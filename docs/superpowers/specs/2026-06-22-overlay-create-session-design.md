# Overlay Create Session

Add a create-session form to the overlay (ctrl+b w) and the standalone new-session shortcut (ctrl+b c). The form has two fields: session name and repo path, with the repo field offering fuzzy autocomplete from discovered repos.

## Component: `createSessionModel`

A new bubbletea model in `internal/client/createinput.go`.

### Fields

**Name** — `textinput.Model`, char limit 64, placeholder `session-name`. Intercepts space keypresses and inserts `-` instead. Same validation as the existing `nameInputModel`.

**Repo** — `textinput.Model` with a suggestions dropdown rendered below it. Defaults to the current session's repo path (when available) or empty. As the user types, the dropdown filters to matching repos using case-insensitive substring matching. Arrow keys navigate the dropdown; enter on a highlighted suggestion fills the field. If no suggestion is selected, the raw text is used as a literal path.

### Navigation

- Tab / Shift+Tab moves focus between fields
- Enter on the name field moves focus to repo
- Enter on the repo field (with no dropdown selection active) submits the form
- Esc cancels and returns to the previous state

### Layout

Centered panel, same style as the existing `nameInputModel`:

```
Create Session

  Name: my-feature-branch
  Repo: ~/Code/gra|
        +--------------------------+
        |> graith   ~/Code/graith  |
        |  grafana  ~/Code/grafana |
        +--------------------------+

  tab next field  enter confirm  esc cancel
```

### Space-to-dash

The Update method intercepts `tea.KeyPressMsg` for the name field: if the key is a space, it inserts `-` into the textinput instead. The existing `nameInputModel` (used by fork) gets the same treatment for consistency.

## Repo Discovery

At form creation time, build the suggestion list by merging two sources:

1. **`allowed_repo_paths`** — for each path, single-level `os.ReadDir`, check for `.git` subdirectory. Collect matching dirs as repo candidates.
2. **Existing sessions** — extract unique `RepoPath` values from the current session list.

Deduplicate by resolved absolute path. Sort alphabetically by basename. Display as `basename  ~/shortened/path`.

Runs once when the form opens. No background scanning or caching. For typical setups (a `~/Code` dir with fewer than 100 repos), this completes in under a millisecond.

If `allowed_repo_paths` is empty, only session-derived repos appear. If there are no sessions either, the dropdown is empty and the user types a path manually.

## Integration: Overlay

New `stateCreate` added to `overlayState`. Pressing `n` in the session list enters this state, rendering the create form instead of the session list. On submit, the overlay returns an `OverlayResult` with `Action: "create"` and the new create fields. On cancel (esc), returns to `stateList`.

`OverlayResult` gets two new fields:

```go
type OverlayResult struct {
    Action         string
    SessionID      string
    CreateName     string
    CreateRepoPath string
    Collapsed      map[string]bool
}
```

## Integration: Attach Loop

The `ResultOverlay` case in `attach.go` checks for `Action == "create"` and sends a `CreateMsg` to the daemon, same as the current `ResultNewSession` handler does.

The existing `ResultNewSession` / `ctrl+b c` handler is updated to use the new create form (via `RunCreateInput`) instead of `RunNameInput`, so both paths share the same component.

## `RunCreateInput` Function

New exported function parallel to `RunNameInput`:

```go
func RunCreateInput(defaultRepo string, repos []RepoSuggestion) (name, repoPath string)
```

Launches the create form as a standalone bubbletea program. Returns `("", "")` on cancel.

`RepoSuggestion` is a simple struct:

```go
type RepoSuggestion struct {
    Name string // basename (e.g. "graith")
    Path string // absolute path (e.g. "/Users/dougal/Code/graith")
}
```

## Repo Discovery Function

```go
func DiscoverRepos(allowedPaths []string, sessions []protocol.SessionInfo) []RepoSuggestion
```

Lives in `internal/client/createinput.go` (or a small helper file). Scans allowed paths one level deep for `.git` dirs, merges with session repo paths, deduplicates, and sorts.

## Files Changed

| File | Change |
|------|--------|
| `internal/client/createinput.go` | New file: `createSessionModel`, `RunCreateInput`, `DiscoverRepos`, `RepoSuggestion` |
| `internal/client/overlay.go` | Add `stateCreate`, wire `n` key, embed create model, handle submit/cancel |
| `internal/client/nameinput.go` | Add space-to-dash interception for consistency |
| `internal/cli/attach.go` | Handle `OverlayResult.Action == "create"`, update `ResultNewSession` to use new form |

## Edge Cases

- **Empty repo field on submit**: the form requires a non-empty repo path. If the repo field is blank, submit is a no-op (same as how the name field rejects empty input today).
- **Invalid repo path**: the daemon's create handler already validates the repo path and returns an error. The attach loop already handles `CreateMsg` errors by printing the message and re-attaching to the current session.
- **No allowed_repo_paths configured, no sessions**: the dropdown is empty. The user types a path manually. This is fine — it's no worse than the current `gr new` CLI experience.

## Out of Scope

- Agent type selection (can be added later as a third field)
- Model selection
- Prompt input
- Persistent "recent repos" storage (session list + allowed_repo_paths covers this)
