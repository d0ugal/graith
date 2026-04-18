package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Live-updating dashboard of all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		sessions, err := fetchSessions()
		if err != nil {
			return err
		}

		for {
			result := client.RunDashboard(sessions, func() []protocol.SessionInfo {
				s, err := fetchSessions()
				if err != nil {
					return nil
				}
				return s
			})
			if result == nil {
				return nil
			}

			switch result.Action {
			case "attach":
				c, err := freshClient()
				if err != nil {
					return err
				}
				err = runAttachByID(c, result.SessionID)
				c.Close()
				return err

			case "delete":
				if err := sendAction("delete", protocol.DeleteMsg{SessionID: result.SessionID}); err != nil {
					return err
				}

			case "stop":
				if err := sendAction("stop", protocol.StopMsg{SessionID: result.SessionID}); err != nil {
					return err
				}

			case "resume":
				if err := sendAction("resume", protocol.ResumeMsg{SessionID: result.SessionID}); err != nil {
					return err
				}

			default:
				return nil
			}

			sessions, err = fetchSessions()
			if err != nil {
				return err
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
}

func fetchSessions() ([]protocol.SessionInfo, error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}
	return list.Sessions, nil
}

func sendAction(msgType string, payload any) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	c.SendControl(msgType, payload)
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		return fmt.Errorf("%s", e.Message)
	}
	return nil
}
