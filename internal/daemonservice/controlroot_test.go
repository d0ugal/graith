package daemonservice

import (
	"path/filepath"
	"testing"
)

func TestControlRootAtReceiptRoot(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		receiptRoot string
		want        string
	}{
		"appends bootstrap directory": {
			receiptRoot: filepath.Join("/tmp", "braw", "control"),
			want:        filepath.Join("/tmp", "braw", "control", "bootstrap"),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := controlRootAtReceiptRoot(test.receiptRoot); got != test.want {
				t.Fatalf("controlRootAtReceiptRoot() = %q, want %q", got, test.want)
			}
		})
	}
}
