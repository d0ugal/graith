package hookoutput

import "encoding/json"

// marshalString marshals v to a JSON string. The values passed here are fixed
// hook-response structs and string maps that cannot fail to marshal, so a
// (theoretically impossible) error yields an empty string rather than being
// silently discarded.
func marshalString(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}

	return string(out)
}

// claudeHookOutput is Claude Code's hook stdout envelope for injecting model
// context. The nested hookSpecificOutput.additionalContext becomes a
// hook_additional_context attachment fed to the model; a top-level
// systemMessage (which graith emitted previously) is only a user-facing warning
// banner and never reaches the model. hookEventName is required and must match
// the firing event exactly. See claude-code src/utils/hooks.ts.
type claudeHookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// InboxContext returns the hook stdout JSON that surfaces unread inbox messages
// to an agent at a lifecycle event (e.g. "SessionStart"). For Claude Code the
// context must be delivered via hookSpecificOutput.additionalContext so it
// actually reaches the model — a plain systemMessage is shown to the human only.
// Other agents keep the systemMessage form they already consume.
func InboxContext(agent, event, context string) string {
	switch agent {
	case "claude", "codex":
		// Both Claude and current Codex deliver model-visible context through
		// hookSpecificOutput.additionalContext; their universal systemMessage is
		// only a user-facing banner. (Codex's *CommandOutputWire types carry
		// additionalContext under hookSpecificOutput — issue #1183.)
		var resp claudeHookOutput

		resp.HookSpecificOutput.HookEventName = event
		resp.HookSpecificOutput.AdditionalContext = context

		return marshalString(resp)
	default:
		return marshalString(map[string]string{"systemMessage": context})
	}
}
