package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestNewPromptAndPromptFileMutuallyExclusive(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("file prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldPrompt, oldPromptFile := newPrompt, newPromptFile
	oldCfg := cfg

	defer func() {
		newPrompt, newPromptFile = oldPrompt, oldPromptFile
		cfg = oldCfg
	}()

	cfg = config.Default()
	newPrompt = "inline prompt"
	newPromptFile = promptFile

	err := newCmd.RunE(newCmd, []string{"kirk"})
	if err == nil {
		t.Fatal("expected error when both --prompt and --prompt-file are set")
	}

	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want it to mention 'mutually exclusive'", err)
	}
}

func TestNewRejectsInvalidLabelBeforeConnecting(t *testing.T) {
	oldLabels, oldCfg := newLabels, cfg

	t.Cleanup(func() { newLabels, cfg = oldLabels, oldCfg })

	cfg = config.Default()
	newLabels = []string{"   "}

	if err := newCmd.RunE(newCmd, []string{"braw"}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("new with empty label error = %v", err)
	}
}

func TestNewReadOnlyRejectsConflictingModesBeforeConnecting(t *testing.T) {
	oldReadOnly, oldNoRepo, oldMirror, oldInPlace := newReadOnly, newNoRepo, newMirror, newInPlace
	oldPrompt, oldPromptFile, oldLabels, oldCfg := newPrompt, newPromptFile, newLabels, cfg

	t.Cleanup(func() {
		newReadOnly, newNoRepo, newMirror, newInPlace = oldReadOnly, oldNoRepo, oldMirror, oldInPlace
		newPrompt, newPromptFile, newLabels, cfg = oldPrompt, oldPromptFile, oldLabels, oldCfg
	})

	tests := map[string]struct {
		noRepo  bool
		mirror  string
		inPlace bool
		want    string
	}{
		"no repo":  {noRepo: true, want: "--read-only and --no-repo"},
		"mirror":   {mirror: "subject", want: "--read-only and --mirror"},
		"in place": {inPlace: true, want: "--read-only and --in-place"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg = config.Default()
			newReadOnly = true
			newNoRepo = test.noRepo
			newMirror = test.mirror
			newInPlace = test.inPlace
			newPrompt = ""
			newPromptFile = ""
			newLabels = nil

			err := newCmd.RunE(newCmd, []string{"braw-reader"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("new --read-only error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCreateLabelsFromRawPreservesOmittedVsExplicit(t *testing.T) {
	tests := map[string]struct {
		raw         []string
		flagChanged bool
		want        []string
		wantNil     bool
	}{
		"unset flag omits labels": {
			wantNil: true,
		},
		"changed flag with values is explicit": {
			raw:         []string{" Urgent ", "release"},
			flagChanged: true,
			want:        []string{"Urgent", "release"},
		},
		"programmatic values are explicit": {
			raw:  []string{"braw"},
			want: []string{"braw"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := createLabelsFromRaw(test.raw, test.flagChanged)
			if err != nil {
				t.Fatal(err)
			}

			if test.wantNil {
				if got != nil {
					t.Fatalf("labels = %#v, want nil", got)
				}

				return
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("labels = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNewLabelFlagIsRepeatable(t *testing.T) {
	registerCommands()

	flag := newCmd.Flags().Lookup("label")
	if flag == nil || flag.Value.Type() != "stringArray" {
		t.Fatalf("new --label = %#v, want repeatable stringArray", flag)
	}
}

func TestNewExperimentalAttachFlagRemoved(t *testing.T) {
	registerCommands()

	if flag := newCmd.Flags().Lookup("experimental-attach"); flag != nil {
		t.Fatalf("new --experimental-attach = %#v, want removed flag", flag)
	}
}

func TestScenarioAddReadOnlyFlagRegistered(t *testing.T) {
	registerCommands()

	if flag := scenarioAddCmd.Flags().Lookup("read-only"); flag == nil || flag.Value.Type() != "bool" {
		t.Fatalf("scenario add --read-only = %#v, want bool flag", flag)
	}
}
