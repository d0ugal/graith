# Externalize Config Defaults

## Problem

Agent CLI flags change across versions. When graith hardcodes agent args (e.g.
`ForkArgs`) in Go structs, users can't fix broken defaults without waiting for a
new graith release. The defaults are invisible — users don't know what graith is
passing to their agents and can't override what they can't see.

## Solution

Move all hardcoded defaults from Go struct literals into an embedded TOML file.
Add CLI commands to inspect, diff, and reset the config. Keep sparse user
configs as the default — they inherit current bundled defaults automatically,
including bug fixes.

## Design

### 1. Embedded default config

A new file `internal/config/default_config.toml` contains all default settings
as well-commented TOML. It is embedded into the binary via `//go:embed` and
replaces the current `Default()` Go struct construction.

```go
//go:embed default_config.toml
var defaultConfigTOML []byte

func Default() *Config {
    cfg := &Config{}
    if err := toml.Unmarshal(defaultConfigTOML, cfg); err != nil {
        panic("invalid embedded default config: " + err.Error())
    }
    return cfg
}

func DefaultTOML() []byte {
    out := make([]byte, len(defaultConfigTOML))
    copy(out, defaultConfigTOML)
    return out
}
```

`DefaultTOML()` returns a defensive copy so callers cannot mutate the
process-global embedded data.

The panic in `Default()` is intentional — a broken embedded config is a
build-time bug. Tests call `Default()` and will catch parse errors before
release.

### 2. No auto-generation on first use

`LoadOrDefault()` remains read-only. It does not write files. When no config
file exists, it returns `Default()` as it does today. Sparse user configs
continue to inherit current bundled defaults — including any bug fixes shipped
in new releases.

`gr doctor` mentions the config file path and how to create one:

```
Config file: ~/.config/graith/config.toml (not found — using built-in defaults)
  → Run `gr config reset` to create a config file
```

### 3. `gr config reset`

Writes the embedded default TOML to the config file path.

Behavior:
- If the config file already exists, prompts: `This will overwrite your config
  at <path>. Continue? [y/N]`
- `--force` flag skips the confirmation prompt (for scripts and CI)
- If stdin is not a TTY and the file exists and `--force` is not set, refuses
  with a clear error message (no hanging on a prompt in CI)
- Writes atomically: temp file in the same directory, then `os.Rename`, mode
  `0600`
- Skips normal config loading (see CLI structure section) so it works even when
  the existing config is malformed
- Creates parent directories via `filepath.Dir(target)` + `os.MkdirAll`
- Respects `--config <path>` if set; otherwise uses the resolved profile path

No `--merge` flag in v1. A safe merge requires either a version marker in
generated configs or a three-way diff to distinguish "user customized this"
from "auto-generated and never touched." This is deferred to a future release
if there is demand.

### 4. `gr config diff`

Shows the effective difference between the user's config and built-in defaults.

Behavior:
- Loads the default config via `Default()` and marshals it to normalized TOML
- Loads the user's effective config via `LoadOrDefault()` and marshals it to
  normalized TOML
- Outputs a unified diff between the two
- If no config file exists, prints to stderr: `No config file found at <path>.
  Using built-in defaults.` and exits 0 (no diff to show)
- If the config file exists but is malformed, prints a parse error to stderr
  and exits 1 (cannot produce an effective diff from an unparseable file)
- Skips normal config loading — reads the file independently so it works when
  the config is missing

This answers "what behavior have I changed from graith's current defaults?"
rather than a textual diff of file contents. It works correctly for sparse
configs because both sides are fully resolved before diffing.

### 5. `gr config show`

Prints the effective (merged) config as TOML.

Behavior:
- Loads the config via `LoadOrDefault()` (defaults merged with user overrides)
- Marshals the result to TOML and prints to stdout
- If no config file exists, prints the built-in defaults
- Shows the effective `Config` struct as TOML — useful for debugging and for
  sharing config in bug reports. Note: derived runtime values (like the
  implicit 1h idle timeout for agents with resume args, or the implicit
  `safehouse` sandbox command) are not included — only the config fields
  themselves

### 6. CLI structure

New `gr config` parent command with three subcommands:

```
gr config reset    # write embedded defaults to config file
gr config diff     # unified diff: effective config vs defaults
gr config show     # print the effective (merged) config
```

All three are implemented in `internal/cli/config.go` (or split into
`config_reset.go`, `config_diff.go`, `config_show.go` if the file gets large).

The `config` parent command itself prints usage help listing the subcommands.

The `config` parent command sets its own `PersistentPreRunE` that resolves
paths but does **not** load or parse the config file. This overrides the root
`PersistentPreRunE` so that `gr config reset` and `gr config diff` work even
when the existing config is malformed. Each subcommand loads the config
independently as needed.

### 7. The embedded TOML file

`internal/config/default_config.toml` is organized by section with comments
explaining each setting. It contains every field from the current `Default()`
function:

```toml
# graith default configuration
# See: gr config diff (to see your changes vs these defaults)
# See: gr config show (to see the effective merged config)

default_agent = "claude"
branch_prefix = "{username}/graith"
fetch_on_create = true

[status_bar]
enabled = true
position = "bottom"

[notifications]
enabled = true
on_approval = true

[approvals]
mode = "prompt"
timeout = "10m"

[keybindings]
prefix = "ctrl+b"
new_session = "c"
fork_session = "f"
delete_session = "x"
detach = "d"
session_list = "w"
next_session = "n"
prev_session = "p"
last_session = "l"
resume_session = "R"
rename_session = ","
search = "/"
scroll_mode = "["
shell = "s"

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]
fork_args = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]
fork_args = ["fork", "{fork_source_agent_session_id}"]

[agents.opencode]
command = "opencode"
args = []
resume_args = ["--session", "{agent_session_id}"]

[agents.agy]
command = "agy"
args = []
resume_args = ["--conversation", "{agent_session_id}"]
```

Comments will be added to explain template variables, keybinding semantics, and
non-obvious defaults. The exact comment text is an implementation detail.

### 8. Legacy config path handling


`LoadOrDefault()` currently falls back to the legacy macOS config path
(`~/Library/Application Support/graith/config.toml`) when the XDG path is
absent. `config show` and `config diff` follow the same resolution — they use
`LoadOrDefault()` which handles the fallback. `config reset` always writes to
the resolved profile path (XDG), not the legacy path.

### 9. Config loading changes

`Load()` continues to work the same way:

1. Parse embedded TOML into a `Config` struct via `Default()`
2. Unmarshal user TOML on top (present fields override, absent fields inherit)
3. Merge agents (user agent fields override defaults, new user agents are added)

No changes to the merge logic. The only change is that `Default()` reads from
the embedded TOML instead of constructing Go struct literals.

### 10. Testing

- **Parse test**: `Default()` succeeds and returns a valid `Config`
- **Value tests**: assert important defaults (claude fork args, keybindings,
  agent commands) match expected values
- **Defensive copy test**: `DefaultTOML()` returns an independent copy;
  mutating the returned slice does not affect subsequent calls
- **Mutation safety test**: mutating maps/slices on a `Default()` result does
  not affect subsequent `Default()` calls
- **Round-trip test**: `Default()` → marshal → unmarshal produces the same
  `Config`
- **`gr config diff`**: test with no config (stderr message, exit 0), with
  unchanged config (no output, exit 0), with modified config (shows diff),
  with malformed config (error, exit 1)
- **`gr config reset`**: test that it writes valid TOML that round-trips;
  test that it works when existing config is malformed; test non-TTY refusal
  without `--force`; test `--force` skips prompt
- **`gr config show`**: test that output is valid TOML matching the effective
  config
- **Legacy config**: test that `show`/`diff` use the legacy macOS config path
  when the XDG path is absent (matching existing `LoadOrDefault` behavior)

## Out of scope

- `gr config reset --merge` — deferred until version-aware migration is
  designed
- Auto-generation of config on first use — intentionally avoided to prevent
  freezing defaults
- Source-level diff (textual diff of TOML files) — effective diff is more useful
  for sparse configs
- `gr config edit` — opens config in `$EDITOR`, nice-to-have for later

## Migration

No migration needed for config loading. Users with existing `config.toml` files
are unaffected — their configs continue to work identically. Users without a
config file continue to get built-in defaults.

The embedded `default_config.toml` includes the corrected Claude `fork_args`
(using `--resume` instead of passing a value after the boolean `--fork-session`
flag). This is an intentional bug fix, not a regression. Tests assert the
corrected value.

The only visible changes are the new `gr config` subcommands and the updated
`gr doctor` output.
