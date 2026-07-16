package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/protocol"
)

// controlConn is the subset of *client.Client that the CLI's control
// round-trip helpers need: send a control message, read the reply, and (for
// callers that own the connection lifecycle) close it. Keeping it an interface
// lets the attach/batch loops be unit-tested with a scripted fake instead of a
// live daemon connection.
type controlConn interface {
	SendControl(msgType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
}

// errorMessage returns the daemon's error text when resp is an "error"
// envelope, or "" for any other envelope type. It centralises the
// decode-the-ErrorMsg-payload dance repeated across every control round-trip.
func errorMessage(resp protocol.Envelope) string {
	if resp.Type != "error" {
		return ""
	}

	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(resp, &e)

	return e.Message
}

// controlOp performs a fire-and-read control round-trip: it sends msgType with
// payload, reads the reply, and maps an "error" envelope to a Go error. A send
// failure is intentionally ignored (matching the historical session-op
// behaviour) — the subsequent read surfaces the broken connection. Used by the
// one-shot session operations (star, stop, restart, rename, delete, restore).
//
// An "error" envelope is reported as an error regardless of its message text,
// so an (unexpected) empty-message error still fails the operation.
func controlOp(c controlConn, msgType string, payload any) error {
	_ = c.SendControl(msgType, payload)

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		return fmt.Errorf("%s", errorMessage(resp))
	}

	return nil
}

// attachDecode sends an attach request for sessionID and decodes the daemon's
// reply into info. Read and decode errors are intentionally swallowed to match
// the attach loop's best-effort reattach behaviour: a failed reattach leaves
// info unchanged and the loop reports the trouble on its next passthrough.
func attachDecode(c controlConn, sessionID string, info *protocol.SessionInfo) {
	_ = c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})

	resp, _ := c.ReadControlResponse()

	_ = protocol.DecodePayload(resp, info)
}

// fetchSessionList requests the session list (msg selects live vs deleted) and
// decodes it. A read or decode failure is returned so callers can abort the
// in-progress transition; a corrupt list frame surfaces as an error rather
// than being silently presented as an empty list.
func fetchSessionList(c controlConn, msg any) (protocol.SessionListMsg, error) {
	_ = c.SendControl("list", msg)

	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.SessionListMsg{}, err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return protocol.SessionListMsg{}, err
	}

	return list, nil
}
