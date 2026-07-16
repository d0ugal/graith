package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8Bytes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"within limit", "braw", 4, "braw"},
		{"ascii", "bothy", 3, "bot..."},
		{"accent boundary", "éclair", 1, "..."},
		{"accent complete", "éclair", 2, "é..."},
		{"emoji boundary", "🙂braw", 3, "..."},
		{"combining boundary", "e\u0301lan", 2, "e..."},
		{"CJK boundary", "编辑器", 4, "编..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateUTF8Bytes(tt.input, tt.limit, "...")
			if got != tt.want {
				t.Errorf("TruncateUTF8Bytes(%q, %d) = %q, want %q", tt.input, tt.limit, got, tt.want)
			}

			if !utf8.ValidString(got) {
				t.Errorf("TruncateUTF8Bytes returned invalid UTF-8: %q", got)
			}

			if strings.HasSuffix(got, "...") && len(strings.TrimSuffix(got, "...")) > tt.limit {
				t.Errorf("retained prefix uses %d bytes, exceeds limit %d", len(strings.TrimSuffix(got, "...")), tt.limit)
			}
		})
	}
}
