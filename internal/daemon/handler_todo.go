package daemon

import (
	"github.com/d0ugal/graith/internal/protocol"
)

// handleTodoAdd adds a todo item to the caller's scope.
func handleTodoAdd(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoAddMsg](msg, send, "invalid todo_add message")
	if !ok {
		return
	}

	if item, err := sm.TodoAddOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo", protocol.TodoResponse{Item: item})
	}
}

// handleTodoList lists the todo items in the caller's scope.
func handleTodoList(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoListMsg](msg, send, "invalid todo_list message")
	if !ok {
		return
	}

	if items, err := sm.TodoListOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo_list", protocol.TodoListResponse{Items: items})
	}
}

// handleTodoClaim atomically claims a todo item (compare-and-set).
func handleTodoClaim(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoClaimMsg](msg, send, "invalid todo_claim message")
	if !ok {
		return
	}

	if resp, err := sm.TodoClaimOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo_claim", resp)
	}
}

// handleTodoTransition transitions a todo item's status (done/block/reopen).
func handleTodoTransition(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoTransitionMsg](msg, send, "invalid todo_transition message")
	if !ok {
		return
	}

	if item, err := sm.TodoTransitionOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo", protocol.TodoResponse{Item: item})
	}
}

// handleTodoUpdate edits a todo item's fields.
func handleTodoUpdate(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoUpdateMsg](msg, send, "invalid todo_update message")
	if !ok {
		return
	}

	if item, err := sm.TodoUpdateOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo", protocol.TodoResponse{Item: item})
	}
}

// handleTodoAssign assigns a todo item to a scope/owner.
func handleTodoAssign(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoAssignMsg](msg, send, "invalid todo_assign message")
	if !ok {
		return
	}

	if item, err := sm.TodoAssignOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo", protocol.TodoResponse{Item: item})
	}
}

// handleTodoRemove removes a todo item.
func handleTodoRemove(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoRemoveMsg](msg, send, "invalid todo_remove message")
	if !ok {
		return
	}

	if err := sm.TodoRemoveOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo_removed", protocol.TodoRemoveMsg{ID: m.ID})
	}
}

// handleTodoExport exports the todo list to a store document, returning its key.
func handleTodoExport(sm *SessionManager, auth authContext, send func(string, any), msg protocol.Envelope) {
	m, ok := decodePayload[protocol.TodoExportMsg](msg, send, "invalid todo_export message")
	if !ok {
		return
	}

	if key, err := sm.TodoExportOp(auth, m); err != nil {
		send("error", protocol.ErrorMsg{Message: err.Error()})
	} else {
		send("todo_export", protocol.TodoExportResponse{Key: key})
	}
}
