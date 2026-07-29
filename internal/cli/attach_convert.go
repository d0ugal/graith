package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/protocol"
)

func attachWithConvertSeed(c controlConn, sessionID string) (protocol.SessionInfo, *protocol.TerminalOwnedAttachSeedMsg, bool, error) {
	var info protocol.SessionInfo

	converted := false

	for {
		_ = c.SendControl("attach", attachMsg(sessionID))

		resp, err := c.ReadControlResponse()
		if err != nil {
			return info, nil, false, err
		}

		switch resp.Type {
		case "error":
			return info, nil, false, fmt.Errorf("%s", errorMessage(resp))

		case "convert_required":
			if converted {
				// We already converted, yet the daemon still reports headless — bail
				// rather than loop forever.
				return info, nil, false, fmt.Errorf("session %q is still headless after convert", sessionID)
			}

			var cr protocol.ConvertRequiredMsg

			_ = protocol.DecodePayload(resp, &cr)

			if !confirmConvert(cr.Name) {
				out.Printf("Aborted\n")
				return info, nil, false, nil
			}

			_ = c.SendControl("attach_convert", protocol.AttachConvertMsg{SessionID: sessionID})

			convResp, err := c.ReadControlResponse()
			if err != nil {
				return info, nil, false, err
			}

			if convResp.Type == "error" {
				return info, nil, false, fmt.Errorf("convert failed: %s", errorMessage(convResp))
			}

			// Require the expected success type rather than treating any non-error
			// reply as success — a malformed/unexpected control frame shouldn't
			// advance the handshake.
			if convResp.Type != "converted" {
				return info, nil, false, fmt.Errorf("unexpected response to attach_convert: %q", convResp.Type)
			}

			converted = true
			// Loop back and attach to the now-interactive session.

		case "attached", "terminal_owned_attached":
			seed, err := decodeAttachResponse(resp, &info)
			if err != nil {
				return info, nil, false, err
			}

			if converted {
				warnUnsandboxedStart(info)
			}

			return info, seed, true, nil

		default:
			return info, nil, false, fmt.Errorf("unexpected response to attach: %q", resp.Type)
		}
	}
}

func decodeAttachResponse(resp protocol.Envelope, info *protocol.SessionInfo) (*protocol.TerminalOwnedAttachSeedMsg, error) {
	switch resp.Type {
	case "error":
		return nil, errors.New(errorMessage(resp))

	case "attached":
		return nil, errors.New(protocol.TerminalOwnedAttachRawResponseMessage)

	case "terminal_owned_attached":
		var seed protocol.TerminalOwnedAttachSeedMsg
		if err := protocol.DecodePayload(resp, &seed); err != nil {
			return nil, fmt.Errorf("decode terminal-owned attach response: %w", err)
		}

		*info = seed.Session

		return &seed, nil

	default:
		return nil, fmt.Errorf("unexpected response to attach: %q", resp.Type)
	}
}

// confirmConvert asks the human whether to convert a headless session to
// interactive. --yes (attachYes) skips the prompt. A non-terminal stdin is
// treated as a decline (fail-safe: don't restart a session unattended).
func confirmConvert(name string) bool {
	if attachYes {
		return true
	}

	out.Printf("%q is a headless session. Attaching will restart it as an interactive session (conversation is preserved). Continue? [y/N] ", name)

	reader := bufio.NewReader(os.Stdin)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	answer = strings.TrimSpace(strings.ToLower(answer))

	return answer == "y" || answer == "yes"
}
