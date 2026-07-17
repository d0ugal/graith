package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestTodoBlockedReason(t *testing.T) {
	item := protocol.TodoItemInfo{
		Status: "blocked", Note: "waiting on review", BlockedBy: []string{"td-braw", "td-canny"},
	}

	got := todoBlockedReason(item)

	want := "dependencies: td-braw,td-canny; waiting on review"
	if got != want {
		t.Fatalf("blocked reason = %q, want %q", got, want)
	}

	if got := todoBlockedReason(protocol.TodoItemInfo{Status: "todo"}); got != "" {
		t.Fatalf("ready item reason = %q, want empty", got)
	}
}
