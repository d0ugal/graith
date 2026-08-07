package sessionlabel

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func FuzzNormalize(f *testing.F) {
	f.Add("Urgent", "urgent", "release")
	f.Add("  Release 7  ", "customer:Brae", "CUSTOMER:brae")
	f.Add("", "braw", "canny")
	f.Add("braw,canny", "dreich", "strath")
	f.Add("braw\nlabel", "bothy", "croft")
	f.Add("K", "K", "e\u0301")

	f.Fuzz(func(t *testing.T, first, second, third string) {
		if len(first)+len(second)+len(third) > 4096 {
			t.Skip()
		}

		got, err := Normalize([]string{first, second, third})
		if err != nil {
			return
		}

		if got == nil {
			t.Fatal("Normalize returned a nil slice on success")
		}

		if len(got) > MaxPerSession {
			t.Fatalf("Normalize returned %d labels, want at most %d", len(got), MaxPerSession)
		}

		for i, label := range got {
			if strings.TrimSpace(label) != label {
				t.Fatalf("label %d = %q, want trimmed", i, label)
			}

			if label == "" {
				t.Fatalf("label %d is empty", i)
			}

			if len(label) > MaxBytes {
				t.Fatalf("label %d = %q exceeds %d bytes", i, label, MaxBytes)
			}

			if strings.ContainsRune(label, ',') {
				t.Fatalf("label %d = %q contains comma", i, label)
			}

			for _, r := range label {
				if unicode.IsControl(r) {
					t.Fatalf("label %d = %q contains control rune %U", i, label, r)
				}
			}

			for j := i + 1; j < len(got); j++ {
				if Equal(label, got[j]) {
					t.Fatalf("labels %d and %d were not deduplicated: %q and %q", i, j, label, got[j])
				}
			}
		}

		gotAgain, err := Normalize(got)
		if err != nil {
			t.Fatalf("Normalize rejected its own output %#v: %v", got, err)
		}

		if !reflect.DeepEqual(gotAgain, got) {
			t.Fatalf("Normalize is not idempotent: %#v then %#v", got, gotAgain)
		}
	})
}
