package sessionlabel

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		want    []string
		wantErr string
	}{
		{name: "empty set", want: []string{}},
		{name: "trim and preserve", labels: []string{"  Release 7  ", "customer:Brae"}, want: []string{"Release 7", "customer:Brae"}},
		{name: "folded duplicate", labels: []string{"Urgent", "urgent", "URGENT"}, want: []string{"Urgent"}},
		{name: "empty label", labels: []string{"  "}, wantErr: "must not be empty"},
		{name: "oversized", labels: []string{strings.Repeat("b", MaxBytes+1)}, wantErr: "exceeds 64 bytes"},
		{name: "comma", labels: []string{"braw,canny"}, wantErr: "contains a comma"},
		{name: "control", labels: []string{"braw\nlabel"}, wantErr: "control character"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.labels)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Normalize() error = %v, want containing %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Normalize()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}

			if got == nil {
				t.Fatal("Normalize() returned nil slice")
			}
		})
	}
}

func TestNormalizeCountLimitAfterDeduplication(t *testing.T) {
	labels := make([]string, 0, MaxPerSession+2)
	for i := range MaxPerSession {
		labels = append(labels, fmt.Sprintf("label-%02d", i))
	}

	labels = append(labels, "LABEL-00")
	if _, err := Normalize(labels); err != nil {
		t.Fatalf("folded duplicate should not exceed limit: %v", err)
	}

	labels = append(labels, "strath")
	if _, err := Normalize(labels); err == nil || !strings.Contains(err.Error(), "maximum of 32") {
		t.Fatalf("Normalize() error = %v, want count limit", err)
	}
}

func TestApply(t *testing.T) {
	got, err := Apply([]string{"Urgent", "release"}, []string{"urgent", "customer:Brae"}, []string{"RELEASE", "missing"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"Urgent", "customer:Brae"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Apply() = %#v, want %#v", got, want)
	}
}

func TestApplyRejectsOverlappingDelta(t *testing.T) {
	if _, err := Apply([]string{"braw"}, []string{"Urgent"}, []string{"urgent"}); err == nil || !strings.Contains(err.Error(), "both added and removed") {
		t.Fatalf("Apply() error = %v, want overlap error", err)
	}
}

func TestEqualUsesUnicodeSimpleFoldingWithoutCanonicalNormalization(t *testing.T) {
	if !Equal("K", "K") {
		t.Fatal("Kelvin sign should compare equal under Unicode simple folding")
	}

	if !Equal("µ", "μ") {
		t.Fatal("micro sign and Greek mu should compare equal in a multi-member fold orbit")
	}

	if Equal("é", "e\u0301") {
		t.Fatal("canonically equivalent spellings must remain distinct without normalization")
	}
}
