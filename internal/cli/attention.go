package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	attentionContext string
	attentionClear   bool
)

type attentionConn interface {
	controlConn
	Close()
}

var attentionConnectFn = func(cfg *config.Config, paths config.Paths, cfgFile string) (attentionConn, error) {
	return client.Connect(cfg, paths, cfgFile)
}

var attentionCmd = &cobra.Command{
	Use:   "attention [text]",
	Short: "Request or clear orchestrator attention in the status bar",
	Long: `Request a persistent status-bar indicator asking the user to return to the
orchestrator session. The request clears automatically when the user attaches
to the orchestrator. If the request is stale when they arrive, the daemon sends
the orchestrator a system inbox notice so it can pick the conversation back up.

Only the orchestrator may create a request. The user or orchestrator may clear
one explicitly with --clear.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if attentionClear {
			if len(args) != 0 {
				return errors.New("--clear does not accept text")
			}

			if attentionContext != "" {
				return errors.New("--clear does not accept --context")
			}

			return nil
		}

		if len(args) == 0 {
			return errors.New("attention text is required")
		}

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAttention(args)
	},
}

type attentionJSONResponse struct {
	Active bool   `json:"active"`
	Text   string `json:"text,omitempty"`
}

func runAttention(args []string) error {
	c, err := attentionConnectFn(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	msg := protocol.OrchestratorAttentionMsg{
		Text:    strings.Join(args, " "),
		Context: attentionContext,
		Clear:   attentionClear,
	}
	if err := c.SendControl("orchestrator_attention", msg); err != nil {
		return err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	if resp.Type != "orchestrator_attention_response" {
		return fmt.Errorf("unexpected response %q from daemon", resp.Type)
	}

	var r protocol.OrchestratorAttentionResponse
	if err := protocol.DecodePayload(resp, &r); err != nil {
		return fmt.Errorf("decode attention response: %w", err)
	}

	return printAttentionResponse(r)
}

func printAttentionResponse(r protocol.OrchestratorAttentionResponse) error {
	if out.IsJSON() {
		return out.JSON(attentionJSONResponse{Active: r.Active, Text: r.Text})
	}

	if r.Active {
		out.Printf("Attention requested: %s\n", r.Text)
	} else {
		out.Printf("Attention request cleared\n")
	}

	return nil
}

func registerAttentionCmd() {
	attentionCmd.Flags().StringVar(&attentionContext, "context", "", "longer context returned to the orchestrator if the user arrives after the request is stale")
	attentionCmd.Flags().BoolVar(&attentionClear, "clear", false, "clear the outstanding orchestrator attention request")
	rootCmd.AddCommand(attentionCmd)
}
