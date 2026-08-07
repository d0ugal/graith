package scenariofile

import (
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(`
version = 1
[scenario]
name = "strath"
goal = "build the brig"
[[sessions]]
name = "ben"
repo = "~/Code/croft"
role = "implementer"
[[sessions]]
name = "bairn"
shared = true
`))
	f.Add([]byte(`
version = 1
[scenario]
name = "strath-prompts"
[[sessions]]
name = "builder"
repo = "~/Code/bothy"
prompt = "Follow the detailed build instructions."
task = "build the brig"
[[sessions]]
name = "reviewer"
repo = "~/Code/croft"
task = "review the brig"
depends_on = ["builder"]
`))
	f.Add([]byte(`
version = 1
[scenario]
name = "strath-triggers"
[[sessions]]
name = "runner"
repo = "~/Code/croft"
role = "implementer"
[[trigger]]
name = "canny-lint"
[trigger.watch]
role = "implementer"
[trigger.action]
type = "message"
body = "changed {changed_files}"
[trigger.action.deliver]
inbox = "{session_name}"
`))
	f.Add([]byte(`version = 2`))
	f.Add([]byte(`{not toml`))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 32768 {
			t.Skip()
		}

		sf, err := Parse(data)
		if err != nil {
			return
		}

		if sf == nil {
			t.Fatal("Parse returned nil file without an error")
		}

		if sf.Version != 1 {
			t.Fatalf("Parse accepted version %d, want 1", sf.Version)
		}

		if sf.Scenario.Name == "" {
			t.Fatal("Parse accepted an empty scenario name")
		}

		if len(sf.Sessions) == 0 {
			t.Fatal("Parse accepted a scenario without sessions")
		}

		inputs, err := SessionInputs(sf)
		if err != nil {
			t.Fatalf("SessionInputs rejected parsed scenario: %v", err)
		}

		if err := ValidateSessionContracts(inputs, config.TodoMaxTitleCeiling); err != nil {
			t.Fatalf("ValidateSessionContracts rejected parsed scenario: %v", err)
		}

		if !HasTemplatedMemberGraph(inputs) {
			if err := ValidateSessionDependencies(inputs); err != nil {
				t.Fatalf("ValidateSessionDependencies rejected parsed scenario: %v", err)
			}
		}

		sfAgain, err := Parse(data)
		if err != nil {
			t.Fatalf("Parse was not deterministic for accepted input: %v", err)
		}

		if sfAgain.Version != sf.Version || sfAgain.Scenario.Name != sf.Scenario.Name || len(sfAgain.Sessions) != len(sf.Sessions) {
			t.Fatalf("Parse changed accepted scenario identity: first=%+v second=%+v", sf, sfAgain)
		}
	})
}
