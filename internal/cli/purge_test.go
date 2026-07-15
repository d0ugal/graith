package cli

import (
	"testing"
)

// TestPurgeCmdArgsValidation exercises the purge Args validator, including the
// --self mode which makes the positional arg optional and is mutually exclusive
// with --children and batch filters. The validator reads package-global flags,
// so save and restore them around each case.
func TestPurgeCmdArgsValidation(t *testing.T) {
	origChildren := purgeChildren
	origBatch := purgeBatch
	origSelf := purgeSelf

	t.Cleanup(func() {
		purgeChildren = origChildren
		purgeBatch = origBatch
		purgeSelf = origSelf
	})

	tests := []struct {
		name     string
		children bool
		self     bool
		batch    batchFlags
		args     []string
		wantErr  bool
	}{
		{name: "children with batch filter rejected", children: true, batch: batchFlags{stopped: true}, args: nil, wantErr: true},
		{name: "batch filter takes no args", batch: batchFlags{repo: "croft"}, args: nil, wantErr: false},
		{name: "batch filter rejects positional arg", batch: batchFlags{repo: "croft"}, args: []string{"braw"}, wantErr: true},
		{name: "children allows zero args", children: true, args: nil, wantErr: false},
		{name: "children allows one arg", children: true, args: []string{"ben"}, wantErr: false},
		{name: "children rejects two args", children: true, args: []string{"ben", "brae"}, wantErr: true},
		{name: "plain requires exactly one arg", args: []string{"braw"}, wantErr: false},
		{name: "plain rejects zero args", args: nil, wantErr: true},
		{name: "self takes no args", self: true, args: nil, wantErr: false},
		{name: "self rejects positional arg", self: true, args: []string{"braw"}, wantErr: true},
		{name: "self with children rejected", self: true, children: true, args: nil, wantErr: true},
		{name: "self with batch filter rejected", self: true, batch: batchFlags{stopped: true}, args: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			purgeChildren = tt.children
			purgeBatch = tt.batch
			purgeSelf = tt.self

			err := purgeCmd.Args(purgeCmd, tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
