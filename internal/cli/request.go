package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
)

// cliRequest connects to the daemon, sends a single control message, reads the
// reply, and surfaces a daemon "error" envelope as a Go error. It is the shared
// connect+send+read+error pattern used by the one-shot control commands.
func cliRequest(msgType string, payload any) (protocol.Envelope, error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return protocol.Envelope{}, err
	}
	defer c.Close()

	_ = c.SendControl(msgType, payload)

	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.Envelope{}, err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return protocol.Envelope{}, fmt.Errorf("%s", e.Message)
	}

	return resp, nil
}
