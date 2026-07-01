package daemon

import (
	"context"

	"github.com/d0ugal/graith/internal/protocol"
)

// handleMsgStreamRead encapsulates the full read/subscribe/ack/follow/wait/detach
// loop shared by msg_sub and msg_inbox handlers. Returns true if the caller
// should return (follow/wait took over reader ownership).
func (sm *SessionManager) handleMsgStreamRead(
	ctx context.Context,
	sendControl func(string, any),
	reader *protocol.FrameReader,
	stream, subscriber string,
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
		sendControl("msg_message", msg)
	}

	if ack && subscriber != "" && len(msgs) > 0 {
		if threadID != "" {
			seqs := make([]int64, len(msgs))
			for i, msg := range msgs {
				seqs[i] = msg.Seq
			}

			sm.messages.AckMessages(stream, subscriber, seqs)
		} else {
			sm.messages.Ack(stream, subscriber, msgs[len(msgs)-1].Seq)
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

				sendControl("msg_message", tmsg)

				if ack && subscriber != "" {
					if threadID != "" {
						sm.messages.AckMessages(stream, subscriber, []int64{tmsg.Seq})
					} else {
						sm.messages.Ack(stream, subscriber, tmsg.Seq)
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
