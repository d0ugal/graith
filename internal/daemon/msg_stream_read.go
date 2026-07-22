package daemon

import (
	"context"
	"fmt"

	"github.com/d0ugal/graith/internal/protocol"
)

// persistMsgStreamAck admits only the durable acknowledgement operation. The
// stream itself may be idle for an arbitrary amount of time and must remain
// observational while it waits for delivery.
func (sm *SessionManager) persistMsgStreamAck(stream, caller, subscriber string, seqs []int64, perMessage bool) error {
	lease, err := sm.beginMutationRequest("msg_ack", caller)
	if err != nil {
		return fmt.Errorf("message delivered but acknowledgement was not persisted; retry the read: %w", err)
	}

	defer sm.endMutationRequest(lease)

	if perMessage {
		err = sm.messages.AckMessages(stream, subscriber, seqs)
	} else {
		err = sm.messages.Ack(stream, subscriber, seqs[len(seqs)-1])
	}

	if err != nil {
		return fmt.Errorf("message delivered but acknowledgement was not persisted; retry the read: %w", err)
	}

	return nil
}

// handleMsgStreamRead encapsulates the full read/subscribe/ack/follow/wait/detach
// loop shared by msg_sub and msg_inbox handlers. Returns true if the caller
// should return (follow/wait took over reader ownership).
func (sm *SessionManager) handleMsgStreamRead(
	ctx context.Context,
	sendControl func(string, any),
	sendControlResult func(string, any) error,
	reader *protocol.FrameReader,
	stream, subscriber string,
	caller string,
	onlyUnread bool, threadID string,
	wait, follow, ack bool,
) bool {
	// Subscribe before reading to avoid missing messages published
	// between Read and Subscribe.
	var (
		sub   chan Message
		unsub func()
	)
	if wait || follow {
		sub, unsub = sm.messages.Subscribe(stream)
	}

	msgs, err := sm.messages.Read(stream, subscriber, onlyUnread, threadID)
	if err != nil {
		if unsub != nil {
			unsub()
		}

		sendControl("error", protocol.ErrorMsg{Message: err.Error()})

		return false
	}

	for _, msg := range msgs {
		if err := sendControlResult("msg_message", msg); err != nil {
			if unsub != nil {
				unsub()
			}

			return false
		}
	}

	if ack && subscriber != "" && len(msgs) > 0 {
		var seqs []int64
		if threadID != "" {
			seqs = make([]int64, len(msgs))
			for i, msg := range msgs {
				seqs[i] = msg.Seq
			}
		} else {
			seqs = []int64{msgs[len(msgs)-1].Seq}
		}

		if err := sm.persistMsgStreamAck(stream, caller, subscriber, seqs, threadID != ""); err != nil {
			sendControl("error", protocol.ErrorMsg{Message: err.Error()})

			if unsub != nil {
				unsub()
			}

			return false
		}
	}

	if !wait && !follow {
		if unsub != nil {
			unsub()
		}

		sendControl("msg_done", struct{}{})

		return false
	}

	if wait && len(msgs) > 0 {
		unsub()
		sendControl("msg_done", struct{}{})

		return false
	}

	sendControl("msg_following", struct{}{})

	detachCh := make(chan struct{})

	go func() {
		for {
			f, err := reader.ReadFrame()
			if err != nil {
				close(detachCh)
				return
			}

			if f.Channel == protocol.ChannelControl {
				ctrl, _ := protocol.DecodeControl(f.Payload)
				if ctrl.Type == "detach" {
					close(detachCh)
					return
				}
			}
		}
	}()

	func() {
		defer unsub()

		for {
			select {
			case tmsg := <-sub:
				if threadID != "" && tmsg.ThreadID != threadID {
					continue
				}

				if err := sendControlResult("msg_message", tmsg); err != nil {
					return
				}

				if ack && subscriber != "" {
					seqs := []int64{tmsg.Seq}
					if err := sm.persistMsgStreamAck(stream, caller, subscriber, seqs, threadID != ""); err != nil {
						sendControl("error", protocol.ErrorMsg{Message: err.Error()})
						return
					}
				}

				if wait {
					sendControl("msg_done", struct{}{})
					return
				}
			case <-detachCh:
				sendControl("msg_done", struct{}{})
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	// The goroutine above owns the reader — returning prevents
	// the outer loop from racing on ReadFrame.
	return true
}
