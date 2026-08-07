package ignore_test

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/ignore"
)

func FuzzLinesMatch(f *testing.F) {
	f.Add("*.log\nbuild/\n!keep.log", "run.log", false)
	f.Add("*.log\nbuild/\n!keep.log", "build", true)
	f.Add("# comment\n\n*.tmp", "# comment", false)
	f.Add(" foo\nbar?", " foo", false)
	f.Add(" foo\nbar?", "bar ", false)
	f.Add("", "anything.go", false)

	f.Fuzz(func(t *testing.T, patternBlock, rel string, isDir bool) {
		if len(patternBlock)+len(rel) > 8192 {
			t.Skip()
		}

		m := ignore.Lines(strings.Split(patternBlock, "\n")...)
		got := m.Match(rel, isDir)

		if gotAgain := m.Match(rel, isDir); gotAgain != got {
			t.Fatalf("Match(%q, %t) was not deterministic: %t then %t", rel, isDir, got, gotAgain)
		}

		if trimmed := strings.Trim(rel, "/"); trimmed != "" && trimmed != "." {
			withExtraSlashes := "/" + rel + "/"
			if gotSlashed := m.Match(withExtraSlashes, isDir); gotSlashed != got {
				t.Fatalf("Match changed after adding boundary slashes: %q=%t, %q=%t", rel, got, withExtraSlashes, gotSlashed)
			}
		}
	})
}
