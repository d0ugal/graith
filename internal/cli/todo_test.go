package cli

import (
	"bytes"
	"encoding/json"
	"strings"
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

func TestWriteTodoListShowsOwnerAndAssignee(t *testing.T) {
	var buf bytes.Buffer

	items := []protocol.TodoItemInfo{{
		ID: "td-braw", Status: "todo", Title: "raise the brig", Assignee: "bairn-id",
	}}

	if err := writeTodoList(&buf, items); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	for _, want := range []string{"OWNER", "ASSIGNEE", "bairn-id"} {
		if !strings.Contains(got, want) {
			t.Errorf("todo list missing %q:\n%s", want, got)
		}
	}

	if strings.Index(got, "OWNER") > strings.Index(got, "ASSIGNEE") {
		t.Errorf("owner/assignee columns in unexpected order:\n%s", got)
	}
}

func TestTodoDependenciesUpdateClearsWithJSONEmptyArray(t *testing.T) {
	msg := todoDependenciesUpdate([]string{"td-braw"})

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `{"id":"td-braw","depends_on":[]}` {
		t.Fatalf("clear message = %s", data)
	}

	var decoded protocol.TodoUpdateMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.DependsOn == nil || len(*decoded.DependsOn) != 0 {
		t.Fatalf("decoded clear dependencies = %#v", decoded.DependsOn)
	}
}
