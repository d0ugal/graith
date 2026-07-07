package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestResumeStatusErr(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{name: "braw", status: "stopped", wantErr: false},
		{name: "dreich", status: "errored", wantErr: false},
		{name: "thrawn", status: "running", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			err := resumeStatusErr(tt.name, tt.status)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resumeStatusErr(%q, %q) = nil, want error", tt.name, tt.status)
				}

				if !strings.Contains(err.Error(), tt.name) {
					t.Errorf("error %q does not mention session name %q", err.Error(), tt.name)
				}

				if !strings.Contains(err.Error(), "already running") {
					t.Errorf("error %q does not explain the session is already running", err.Error())
				}
			} else if err != nil {
				t.Errorf("resumeStatusErr(%q, %q) = %v, want nil", tt.name, tt.status, err)
			}
		})
	}
}

func TestResumeCmdRegistered(t *testing.T) {
	registerCommands()

	var resume *cobra.Command

	for _, c := range rootCmd.Commands() {
		if c.Name() == "resume" {
			resume = c
			break
		}
	}

	if resume == nil {
		t.Fatal("resume command not registered on rootCmd")
	}

	if resume.Flags().Lookup("attach") == nil {
		t.Error("resume command missing --attach flag")
	}

	// resume takes exactly one positional argument (a session name or ID).
	if err := resume.Args(resume, []string{"braw"}); err != nil {
		t.Errorf("resume should accept one arg: %v", err)
	}

	if err := resume.Args(resume, nil); err == nil {
		t.Error("resume should reject zero args")
	}

	if err := resume.Args(resume, []string{"braw", "canny"}); err == nil {
		t.Error("resume should reject two args")
	}
}
