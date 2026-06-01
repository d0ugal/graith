# graith Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build graith — a terminal multiplexer for managing multiple AI coding agent sessions, each in an isolated git worktree.

**Architecture:** Daemon/client over a Unix socket with a custom framing protocol. The daemon owns PTYs and session state. The client has two modes: raw passthrough for agent interaction, and a bubbletea overlay for session management.

**Tech Stack:** Go 1.22+, cobra (CLI), go-toml/v2 (config), adrg/xdg (paths), creack/pty (PTY), charmbracelet/bubbletea + bubbles + lipgloss (TUI)

**Spec:** `docs/superpowers/specs/2026-06-05-graith-design.md`

---

## Milestone 1: Project Foundation

### Task 1: Project scaffolding

**Files:**
- Create: `go.mod`
- Create: `cmd/graith/main.go`
- Create: `internal/version/version.go`

- [ ] **Step 1: Initialize Go module and install dependencies**

```bash
cd /Users/dougalmatthews/Code/graith
go mod init github.com/dougalmatthews/graith
go get github.com/spf13/cobra@latest
go get github.com/pelletier/go-toml/v2@latest
go get github.com/adrg/xdg@latest
go get github.com/creack/pty@latest
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get golang.org/x/term@latest
```

- [ ] **Step 2: Create directory structure**

```bash
mkdir -p cmd/graith
mkdir -p internal/{cli,config,daemon,protocol,pty,git,client,output,version}
```

- [ ] **Step 3: Write version module**

```go
// internal/version/version.go
package version

var (
    Version   = "dev"
    CommitSHA = "unknown"
)
```

- [ ] **Step 4: Write main.go**

```go
// cmd/graith/main.go
package main

import (
    "os"

    "github.com/dougalmatthews/graith/internal/cli"
)

func main() {
    if err := cli.Execute(); err != nil {
        os.Exit(1)
    }
}
```

- [ ] **Step 5: Write minimal root command**

```go
// internal/cli/root.go
package cli

import (
    "fmt"
    "os"

    "github.com/dougalmatthews/graith/internal/version"
    "github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
    Use:     "gr",
    Short:   "graith — AI agent session manager",
    Version: version.Version,
}

func Execute() error {
    return rootCmd.Execute()
}

func init() {
    rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
}
```

- [ ] **Step 6: Verify it builds and runs**

Run: `go build -o gr ./cmd/graith && ./gr --version`
Expected: outputs version string

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "scaffold: initialize Go project with dependencies"
```

---

### Task 2: Configuration

**Files:**
- Create: `internal/config/paths.go`
- Create: `internal/config/config.go`
- Create: `internal/config/template.go`
- Create: `internal/config/paths_test.go`
- Create: `internal/config/config_test.go`
- Create: `internal/config/template_test.go`

- [ ] **Step 1: Write path resolution tests**

```go
// internal/config/paths_test.go
package config

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestResolvePaths(t *testing.T) {
    p := ResolvePaths()

    if !strings.HasSuffix(p.ConfigFile, filepath.Join("graith", "config.toml")) {
        t.Errorf("ConfigFile = %q, want suffix graith/config.toml", p.ConfigFile)
    }
    if !strings.HasSuffix(p.DataDir, "graith") {
        t.Errorf("DataDir = %q, want suffix graith", p.DataDir)
    }
    if !strings.HasSuffix(p.SocketPath, "graith.sock") {
        t.Errorf("SocketPath = %q, want suffix graith.sock", p.SocketPath)
    }
    if !strings.HasSuffix(p.PIDFile, "graith.pid") {
        t.Errorf("PIDFile = %q, want suffix graith.pid", p.PIDFile)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestResolvePaths -v`
Expected: FAIL — `ResolvePaths` not defined

- [ ] **Step 3: Implement path resolution**

```go
// internal/config/paths.go
package config

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/adrg/xdg"
)

const appName = "graith"

type Paths struct {
    ConfigFile string
    DataDir    string
    RuntimeDir string
    SocketPath string
    PIDFile    string
    StateFile  string
    LogDir     string
    DaemonLog  string
}

func ResolvePaths() Paths {
    configFile := filepath.Join(xdg.ConfigHome, appName, "config.toml")
    dataDir := filepath.Join(xdg.DataHome, appName)

    runtimeDir := runtimeDirForGraith()

    return Paths{
        ConfigFile: configFile,
        DataDir:    dataDir,
        RuntimeDir: runtimeDir,
        SocketPath: filepath.Join(runtimeDir, "graith.sock"),
        PIDFile:    filepath.Join(runtimeDir, "graith.pid"),
        StateFile:  filepath.Join(dataDir, "state.json"),
        LogDir:     filepath.Join(dataDir, "logs"),
        DaemonLog:  filepath.Join(dataDir, "daemon.log"),
    }
}

func runtimeDirForGraith() string {
    if d := xdg.RuntimeDir; d != "" {
        return filepath.Join(d, appName)
    }
    if d := os.Getenv("TMPDIR"); d != "" {
        return filepath.Join(d, fmt.Sprintf("graith-%d", os.Getuid()))
    }
    return filepath.Join("/tmp", fmt.Sprintf("graith-%d", os.Getuid()))
}

func (p Paths) EnsureDirs() error {
    dirs := []string{
        filepath.Dir(p.ConfigFile),
        p.DataDir,
        p.RuntimeDir,
        p.LogDir,
    }
    for _, dir := range dirs {
        if err := os.MkdirAll(dir, 0o750); err != nil {
            return fmt.Errorf("create directory %s: %w", dir, err)
        }
    }
    return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestResolvePaths -v`
Expected: PASS

- [ ] **Step 5: Write template expansion tests**

```go
// internal/config/template_test.go
package config

import "testing"

func TestExpand(t *testing.T) {
    vars := TemplateVars{
        Username:       "d0ugal",
        AgentSessionID: "abc-123",
        SessionName:    "fix-bug",
        SessionID:      "a3f2b1c9",
        WorktreePath:   "/tmp/worktree",
    }

    tests := []struct {
        input string
        want  string
    }{
        {"{username}/graith", "d0ugal/graith"},
        {"--session-id {agent_session_id}", "--session-id abc-123"},
        {"no vars here", "no vars here"},
        {"{session_name}-{session_id}", "fix-bug-a3f2b1c9"},
    }

    for _, tt := range tests {
        got, err := Expand(tt.input, vars)
        if err != nil {
            t.Errorf("Expand(%q) error: %v", tt.input, err)
            continue
        }
        if got != tt.want {
            t.Errorf("Expand(%q) = %q, want %q", tt.input, got, tt.want)
        }
    }
}

func TestExpandUnknownVar(t *testing.T) {
    vars := TemplateVars{}
    _, err := Expand("{nonexistent}", vars)
    if err == nil {
        t.Error("Expand with unknown var should return error")
    }
}

func TestExpandSlice(t *testing.T) {
    vars := TemplateVars{Username: "d0ugal", AgentSessionID: "abc"}
    got, err := ExpandSlice([]string{"--resume", "{agent_session_id}"}, vars)
    if err != nil {
        t.Fatal(err)
    }
    if got[0] != "--resume" || got[1] != "abc" {
        t.Errorf("ExpandSlice = %v, want [--resume abc]", got)
    }
}
```

- [ ] **Step 6: Implement template expansion**

```go
// internal/config/template.go
package config

import (
    "fmt"
    "regexp"
)

var varPattern = regexp.MustCompile(`\{(\w+)\}`)

type TemplateVars struct {
    Username       string
    AgentSessionID string
    SessionName    string
    SessionID      string
    WorktreePath   string
}

func (v TemplateVars) toMap() map[string]string {
    return map[string]string{
        "username":         v.Username,
        "agent_session_id": v.AgentSessionID,
        "session_name":     v.SessionName,
        "session_id":       v.SessionID,
        "worktree_path":    v.WorktreePath,
    }
}

func Expand(s string, vars TemplateVars) (string, error) {
    lookup := vars.toMap()
    var expandErr error

    result := varPattern.ReplaceAllStringFunc(s, func(match string) string {
        key := match[1 : len(match)-1]
        val, ok := lookup[key]
        if !ok {
            expandErr = fmt.Errorf("unknown template variable %q in %q", key, s)
            return match
        }
        return val
    })

    return result, expandErr
}

func ExpandSlice(ss []string, vars TemplateVars) ([]string, error) {
    out := make([]string, len(ss))
    for i, s := range ss {
        expanded, err := Expand(s, vars)
        if err != nil {
            return nil, err
        }
        out[i] = expanded
    }
    return out, nil
}
```

- [ ] **Step 7: Run template tests**

Run: `go test ./internal/config/ -run TestExpand -v`
Expected: PASS

- [ ] **Step 8: Write config loading tests**

```go
// internal/config/config_test.go
package config

import (
    "os"
    "path/filepath"
    "testing"
)

func TestLoadConfig(t *testing.T) {
    dir := t.TempDir()
    cfgPath := filepath.Join(dir, "config.toml")

    toml := `
default_agent = "claude"
github_username = "d0ugal"
branch_prefix = "{username}/graith"
scrollback_limit = "100MB"
fetch_on_create = true

[keybindings]
prefix = "ctrl+b"
new_session = "c"
delete_session = "x"
detach = "d"
session_list = "w"
next_session = "n"
prev_session = "p"
resume_session = "R"
rename_session = ","
search = "/"
scroll_mode = "["

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]
`
    os.WriteFile(cfgPath, []byte(toml), 0o644)

    cfg, err := Load(cfgPath)
    if err != nil {
        t.Fatal(err)
    }

    if cfg.DefaultAgent != "claude" {
        t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
    }
    if cfg.GitHubUsername != "d0ugal" {
        t.Errorf("GitHubUsername = %q, want d0ugal", cfg.GitHubUsername)
    }
    if cfg.Keybindings.Prefix != "ctrl+b" {
        t.Errorf("Prefix = %q, want ctrl+b", cfg.Keybindings.Prefix)
    }

    claude, ok := cfg.Agents["claude"]
    if !ok {
        t.Fatal("missing claude agent")
    }
    if claude.Command != "claude" {
        t.Errorf("claude command = %q", claude.Command)
    }
    if len(claude.Args) != 2 || claude.Args[0] != "--session-id" {
        t.Errorf("claude args = %v", claude.Args)
    }
}

func TestLoadConfigMissing(t *testing.T) {
    _, err := Load("/nonexistent/config.toml")
    if err == nil {
        t.Error("expected error for missing file")
    }
}

func TestDefaultConfig(t *testing.T) {
    cfg := Default()
    if cfg.DefaultAgent != "claude" {
        t.Errorf("default agent = %q, want claude", cfg.DefaultAgent)
    }
    if _, ok := cfg.Agents["claude"]; !ok {
        t.Error("default config missing claude agent")
    }
}
```

- [ ] **Step 9: Implement config loading**

```go
// internal/config/config.go
package config

import (
    "fmt"
    "os"

    "github.com/pelletier/go-toml/v2"
)

type Config struct {
    DefaultAgent    string           `toml:"default_agent"`
    GitHubUsername  string           `toml:"github_username"`
    BranchPrefix    string           `toml:"branch_prefix"`
    ScrollbackLimit string           `toml:"scrollback_limit"`
    FetchOnCreate   bool             `toml:"fetch_on_create"`
    Keybindings     Keybindings      `toml:"keybindings"`
    Agents          map[string]Agent `toml:"agents"`
}

type Keybindings struct {
    Prefix        string `toml:"prefix"`
    NewSession    string `toml:"new_session"`
    DeleteSession string `toml:"delete_session"`
    Detach        string `toml:"detach"`
    SessionList   string `toml:"session_list"`
    NextSession   string `toml:"next_session"`
    PrevSession   string `toml:"prev_session"`
    ResumeSession string `toml:"resume_session"`
    RenameSession string `toml:"rename_session"`
    Search        string `toml:"search"`
    ScrollMode    string `toml:"scroll_mode"`
    Shell         string `toml:"shell"`
}

type Agent struct {
    Command    string            `toml:"command"`
    Args       []string          `toml:"args"`
    ResumeArgs []string          `toml:"resume_args"`
    Env        map[string]string `toml:"env"`
}

func Load(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading config: %w", err)
    }

    cfg := Default()
    if err := toml.Unmarshal(data, cfg); err != nil {
        return nil, fmt.Errorf("parsing config %s: %w", path, err)
    }

    return cfg, nil
}

func Default() *Config {
    return &Config{
        DefaultAgent:    "claude",
        BranchPrefix:    "{username}/graith",
        ScrollbackLimit: "100MB",
        FetchOnCreate:   true,
        Keybindings: Keybindings{
            Prefix:        "ctrl+b",
            NewSession:    "c",
            DeleteSession: "x",
            Detach:        "d",
            SessionList:   "w",
            NextSession:   "n",
            PrevSession:   "p",
            ResumeSession: "R",
            RenameSession: ",",
            Search:        "/",
            ScrollMode:    "[",
            Shell:         "s",
        },
        Agents: map[string]Agent{
            "claude": {
                Command:    "claude",
                Args:       []string{"--session-id", "{agent_session_id}"},
                ResumeArgs: []string{"--resume", "{agent_session_id}"},
            },
            "codex": {
                Command:    "codex",
                Args:       []string{},
                ResumeArgs: []string{"resume", "--last"},
            },
            "opencode": {
                Command:    "opencode",
                Args:       []string{},
                ResumeArgs: []string{"--session", "{agent_session_id}"},
            },
            "agy": {
                Command:    "agy",
                Args:       []string{},
                ResumeArgs: []string{"--conversation", "{agent_session_id}"},
            },
        },
    }
}

func LoadOrDefault(path string) *Config {
    if path == "" {
        p := ResolvePaths()
        path = p.ConfigFile
    }
    cfg, err := Load(path)
    if err != nil {
        return Default()
    }
    return cfg
}
```

- [ ] **Step 10: Run all config tests**

Run: `go test ./internal/config/ -v`
Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add -A && git commit -m "feat: add config loading, path resolution, and template expansion"
```

---

### Task 3: Output module

**Files:**
- Create: `internal/output/output.go`
- Create: `internal/output/output_test.go`

- [ ] **Step 1: Write output tests**

```go
// internal/output/output_test.go
package output

import (
    "bytes"
    "encoding/json"
    "testing"
)

func TestJSONOutput(t *testing.T) {
    var buf bytes.Buffer
    w := &Writer{jsonMode: true, out: &buf, errOut: &buf}

    type data struct {
        Name string `json:"name"`
    }
    w.JSON(data{Name: "test"})

    var got data
    if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
        t.Fatalf("unmarshal: %v\nbuf: %s", err, buf.String())
    }
    if got.Name != "test" {
        t.Errorf("Name = %q, want test", got.Name)
    }
}

func TestHumanOutput(t *testing.T) {
    var buf bytes.Buffer
    w := &Writer{jsonMode: false, out: &buf, errOut: &buf}

    w.Print("hello %s\n", "world")

    if buf.String() != "hello world\n" {
        t.Errorf("output = %q", buf.String())
    }
}

func TestPrintSuppressedInJSONMode(t *testing.T) {
    var buf bytes.Buffer
    w := &Writer{jsonMode: true, out: &buf, errOut: &buf}

    w.Print("should not appear")

    if buf.Len() != 0 {
        t.Errorf("Print should be suppressed in JSON mode, got %q", buf.String())
    }
}
```

- [ ] **Step 2: Implement output writer**

```go
// internal/output/output.go
package output

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
)

type Writer struct {
    jsonMode bool
    out      io.Writer
    errOut   io.Writer
}

func New(jsonMode bool) *Writer {
    return &Writer{
        jsonMode: jsonMode,
        out:      os.Stdout,
        errOut:   os.Stderr,
    }
}

func (w *Writer) Print(format string, args ...any) {
    if !w.jsonMode {
        fmt.Fprintf(w.out, format, args...)
    }
}

func (w *Writer) JSON(v any) error {
    enc := json.NewEncoder(w.out)
    enc.SetIndent("", "  ")
    return enc.Encode(v)
}

func (w *Writer) Error(err error) {
    if w.jsonMode {
        type jsonErr struct {
            Error string `json:"error"`
        }
        enc := json.NewEncoder(w.errOut)
        enc.Encode(jsonErr{Error: err.Error()})
        return
    }
    fmt.Fprintf(w.errOut, "error: %v\n", err)
}

func (w *Writer) IsJSON() bool {
    return w.jsonMode
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/output/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add output module for JSON/human-readable output"
```

---

## Milestone 2: Protocol & Git

### Task 4: Frame protocol

**Files:**
- Create: `internal/protocol/frame.go`
- Create: `internal/protocol/frame_test.go`

- [ ] **Step 1: Write frame round-trip tests**

```go
// internal/protocol/frame_test.go
package protocol

import (
    "bytes"
    "testing"
)

func TestFrameRoundTrip(t *testing.T) {
    var buf bytes.Buffer

    w := NewFrameWriter(&buf)
    r := NewFrameReader(&buf)

    if err := w.WriteFrame(ChannelControl, []byte(`{"type":"list"}`)); err != nil {
        t.Fatal(err)
    }
    if err := w.WriteFrame(ChannelData, []byte("hello pty")); err != nil {
        t.Fatal(err)
    }

    f1, err := r.ReadFrame()
    if err != nil {
        t.Fatal(err)
    }
    if f1.Channel != ChannelControl || string(f1.Payload) != `{"type":"list"}` {
        t.Errorf("frame 1: channel=%d payload=%q", f1.Channel, f1.Payload)
    }

    f2, err := r.ReadFrame()
    if err != nil {
        t.Fatal(err)
    }
    if f2.Channel != ChannelData || string(f2.Payload) != "hello pty" {
        t.Errorf("frame 2: channel=%d payload=%q", f2.Channel, f2.Payload)
    }
}

func TestFrameEmptyPayload(t *testing.T) {
    var buf bytes.Buffer
    w := NewFrameWriter(&buf)
    r := NewFrameReader(&buf)

    w.WriteFrame(ChannelControl, []byte{})
    f, err := r.ReadFrame()
    if err != nil {
        t.Fatal(err)
    }
    if len(f.Payload) != 0 {
        t.Errorf("expected empty payload, got %d bytes", len(f.Payload))
    }
}

func TestFrameTooLarge(t *testing.T) {
    var buf bytes.Buffer
    w := NewFrameWriter(&buf)

    big := make([]byte, MaxPayload+1)
    err := w.WriteFrame(ChannelData, big)
    if err == nil {
        t.Error("expected error for oversized payload")
    }
}
```

- [ ] **Step 2: Implement frame protocol**

```go
// internal/protocol/frame.go
package protocol

import (
    "encoding/binary"
    "fmt"
    "io"
)

const (
    ChannelControl = byte(0x00)
    ChannelData    = byte(0x01)
    MaxPayload     = 4 * 1024 * 1024 // 4 MiB
    headerSize     = 5
)

type Frame struct {
    Channel byte
    Payload []byte
}

type FrameWriter struct {
    w   io.Writer
    hdr [headerSize]byte
}

func NewFrameWriter(w io.Writer) *FrameWriter {
    return &FrameWriter{w: w}
}

func (fw *FrameWriter) WriteFrame(channel byte, payload []byte) error {
    if len(payload) > MaxPayload {
        return fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), MaxPayload)
    }
    fw.hdr[0] = channel
    binary.BigEndian.PutUint32(fw.hdr[1:], uint32(len(payload)))

    if _, err := fw.w.Write(fw.hdr[:]); err != nil {
        return fmt.Errorf("write frame header: %w", err)
    }
    if len(payload) > 0 {
        if _, err := fw.w.Write(payload); err != nil {
            return fmt.Errorf("write frame payload: %w", err)
        }
    }
    return nil
}

type FrameReader struct {
    r   io.Reader
    hdr [headerSize]byte
}

func NewFrameReader(r io.Reader) *FrameReader {
    return &FrameReader{r: r}
}

func (fr *FrameReader) ReadFrame() (Frame, error) {
    if _, err := io.ReadFull(fr.r, fr.hdr[:]); err != nil {
        return Frame{}, err
    }

    channel := fr.hdr[0]
    length := binary.BigEndian.Uint32(fr.hdr[1:])

    if length > MaxPayload {
        return Frame{}, fmt.Errorf("frame too large: %d bytes (max %d)", length, MaxPayload)
    }

    payload := make([]byte, length)
    if length > 0 {
        if _, err := io.ReadFull(fr.r, payload); err != nil {
            return Frame{}, fmt.Errorf("read frame payload: %w", err)
        }
    }

    return Frame{Channel: channel, Payload: payload}, nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/protocol/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add frame protocol for daemon/client communication"
```

---

### Task 5: Control messages

**Files:**
- Create: `internal/protocol/messages.go`
- Create: `internal/protocol/messages_test.go`

- [ ] **Step 1: Write message encoding tests**

```go
// internal/protocol/messages_test.go
package protocol

import (
    "testing"
    "time"
)

func TestEncodeDecodeControl(t *testing.T) {
    handshake := HandshakeMsg{
        Version:      "1.0",
        ClientID:     "test-client",
        TerminalSize: [2]uint16{80, 24},
        Cwd:          "/home/user/repo",
    }

    data, err := EncodeControl("handshake", handshake)
    if err != nil {
        t.Fatal(err)
    }

    msg, err := DecodeControl(data)
    if err != nil {
        t.Fatal(err)
    }

    if msg.Type != "handshake" {
        t.Errorf("Type = %q, want handshake", msg.Type)
    }

    var got HandshakeMsg
    if err := DecodePayload(msg, &got); err != nil {
        t.Fatal(err)
    }
    if got.ClientID != "test-client" {
        t.Errorf("ClientID = %q", got.ClientID)
    }
    if got.Cwd != "/home/user/repo" {
        t.Errorf("Cwd = %q", got.Cwd)
    }
}

func TestSessionInfoRoundTrip(t *testing.T) {
    session := SessionInfo{
        ID:        "a3f2b1c9",
        Name:      "fix-auth-bug",
        RepoPath:  "/home/user/repo",
        RepoName:  "repo",
        Branch:    "d0ugal/graith/fix-auth-bug-a3f2b1c9",
        Agent:     "claude",
        Status:    "running",
        CreatedAt: time.Now().UTC().Format(time.RFC3339),
    }

    data, err := EncodeControl("session_update", session)
    if err != nil {
        t.Fatal(err)
    }

    msg, _ := DecodeControl(data)
    var got SessionInfo
    DecodePayload(msg, &got)

    if got.ID != "a3f2b1c9" || got.Name != "fix-auth-bug" {
        t.Errorf("session = %+v", got)
    }
}
```

- [ ] **Step 2: Implement control messages**

```go
// internal/protocol/messages.go
package protocol

import (
    "encoding/json"
    "fmt"
)

type Envelope struct {
    Type    string          `json:"type"`
    Payload json.RawMessage `json:"payload,omitempty"`
}

func EncodeControl(msgType string, payload any) ([]byte, error) {
    p, err := json.Marshal(payload)
    if err != nil {
        return nil, fmt.Errorf("marshal payload: %w", err)
    }
    return json.Marshal(Envelope{Type: msgType, Payload: p})
}

func DecodeControl(raw []byte) (Envelope, error) {
    var m Envelope
    if err := json.Unmarshal(raw, &m); err != nil {
        return Envelope{}, fmt.Errorf("decode control: %w", err)
    }
    return m, nil
}

func DecodePayload(m Envelope, target any) error {
    return json.Unmarshal(m.Payload, target)
}

// --- Client → Daemon messages ---

type HandshakeMsg struct {
    Version      string     `json:"version"`
    ClientID     string     `json:"client_id"`
    TerminalSize [2]uint16  `json:"terminal_size"`
    Cwd          string     `json:"cwd"`
}

type CreateMsg struct {
    Name     string `json:"name"`
    Agent    string `json:"agent"`
    RepoPath string `json:"repo_path"`
    Base     string `json:"base,omitempty"`
}

type AttachMsg struct {
    SessionID string `json:"session_id"`
}

type DeleteMsg struct {
    SessionID string `json:"session_id"`
}

type RenameMsg struct {
    SessionID string `json:"session_id"`
    NewName   string `json:"new_name"`
}

type ResumeMsg struct {
    SessionID string `json:"session_id"`
}

type ResizeMsg struct {
    Cols uint16 `json:"cols"`
    Rows uint16 `json:"rows"`
}

type ScrollbackMsg struct {
    SessionID string `json:"session_id"`
    Lines     int    `json:"lines"`
}

type SearchMsg struct {
    SessionID string `json:"session_id"`
    Query     string `json:"query"`
    Direction string `json:"direction"`
}

type ConfirmResponseMsg struct {
    ConfirmID string `json:"confirm_id"`
    Confirmed bool   `json:"confirmed"`
}

// --- Daemon → Client messages ---

type HandshakeOkMsg struct {
    Version string `json:"version"`
}

type HandshakeErrMsg struct {
    Reason string `json:"reason"`
}

type SessionListMsg struct {
    Sessions []SessionInfo `json:"sessions"`
}

type SessionInfo struct {
    ID             string `json:"id"`
    Name           string `json:"name"`
    RepoPath       string `json:"repo_path"`
    RepoName       string `json:"repo_name"`
    WorktreePath   string `json:"worktree_path"`
    Branch         string `json:"branch"`
    Agent          string `json:"agent"`
    AgentSessionID string `json:"agent_session_id,omitempty"`
    Status         string `json:"status"`
    ExitCode       *int   `json:"exit_code,omitempty"`
    CreatedAt      string `json:"created_at"`
}

type DetachedMsg struct {
    Reason string `json:"reason"`
}

type ErrorMsg struct {
    Message string `json:"message"`
}

type ConfirmMsg struct {
    ConfirmID string `json:"confirm_id"`
    Prompt    string `json:"prompt"`
}

type SessionUpdateMsg struct {
    SessionID string `json:"session_id"`
    Status    string `json:"status"`
    ExitCode  *int   `json:"exit_code,omitempty"`
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/protocol/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add control message types for daemon/client protocol"
```

---

### Task 6: Git operations

**Files:**
- Create: `internal/git/git.go`
- Create: `internal/git/worktree.go`
- Create: `internal/git/branch.go`
- Create: `internal/git/username.go`
- Create: `internal/git/git_test.go`

- [ ] **Step 1: Write git runner tests**

```go
// internal/git/git_test.go
package git

import (
    "os"
    "os/exec"
    "path/filepath"
    "testing"
)

func setupTestRepo(t *testing.T) string {
    t.Helper()
    dir := t.TempDir()
    run := func(args ...string) {
        cmd := exec.Command("git", args...)
        cmd.Dir = dir
        cmd.Env = append(os.Environ(),
            "GIT_AUTHOR_NAME=test",
            "GIT_AUTHOR_EMAIL=test@test.com",
            "GIT_COMMITTER_NAME=test",
            "GIT_COMMITTER_EMAIL=test@test.com",
        )
        if out, err := cmd.CombinedOutput(); err != nil {
            t.Fatalf("git %v: %v\n%s", args, err, out)
        }
    }
    run("init", "-b", "main")
    os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0o644)
    run("add", ".")
    run("commit", "-m", "initial")
    return dir
}

func TestRunOutput(t *testing.T) {
    dir := setupTestRepo(t)
    out, err := RunOutput(dir, "rev-parse", "--is-inside-work-tree")
    if err != nil {
        t.Fatal(err)
    }
    if out != "true" {
        t.Errorf("output = %q, want true", out)
    }
}

func TestRunCheck(t *testing.T) {
    dir := setupTestRepo(t)

    if !RunCheck(dir, "rev-parse", "--is-inside-work-tree") {
        t.Error("expected true for valid repo")
    }

    if RunCheck("/nonexistent", "status") {
        t.Error("expected false for nonexistent dir")
    }
}

func TestRefExists(t *testing.T) {
    dir := setupTestRepo(t)

    if !RefExists(dir, "main") {
        t.Error("main branch should exist")
    }
    if RefExists(dir, "nonexistent-branch") {
        t.Error("nonexistent branch should not exist")
    }
}

func TestHasUncommittedChanges(t *testing.T) {
    dir := setupTestRepo(t)

    dirty, err := HasUncommittedChanges(dir)
    if err != nil {
        t.Fatal(err)
    }
    if dirty {
        t.Error("clean repo should not be dirty")
    }

    os.WriteFile(filepath.Join(dir, "new.txt"), []byte("change"), 0o644)
    dirty, err = HasUncommittedChanges(dir)
    if err != nil {
        t.Fatal(err)
    }
    if !dirty {
        t.Error("repo with new file should be dirty")
    }
}

func TestIsInsideGitRepo(t *testing.T) {
    dir := setupTestRepo(t)

    if !IsInsideGitRepo(dir) {
        t.Error("should detect git repo")
    }
    if IsInsideGitRepo(t.TempDir()) {
        t.Error("should not detect non-repo as git repo")
    }
}

func TestParseGitHubUsernameSSH(t *testing.T) {
    u, ok := ParseGitHubUsername("git@github.com:d0ugal/graith.git")
    if !ok || u != "d0ugal" {
        t.Errorf("got %q, %v", u, ok)
    }
}

func TestParseGitHubUsernameHTTPS(t *testing.T) {
    u, ok := ParseGitHubUsername("https://github.com/d0ugal/graith.git")
    if !ok || u != "d0ugal" {
        t.Errorf("got %q, %v", u, ok)
    }
}
```

- [ ] **Step 2: Implement git runner**

```go
// internal/git/git.go
package git

import (
    "bytes"
    "fmt"
    "os/exec"
    "strings"
)

func Run(dir string, args ...string) (string, string, error) {
    cmd := exec.Command("git", args...)
    cmd.Dir = dir

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    err := cmd.Run()
    return strings.TrimSpace(stdout.String()),
        strings.TrimSpace(stderr.String()),
        err
}

func RunOutput(dir string, args ...string) (string, error) {
    stdout, stderr, err := Run(dir, args...)
    if err != nil {
        return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
    }
    return stdout, nil
}

func RunCheck(dir string, args ...string) bool {
    cmd := exec.Command("git", args...)
    cmd.Dir = dir
    return cmd.Run() == nil
}

func IsInsideGitRepo(dir string) bool {
    return RunCheck(dir, "rev-parse", "--is-inside-work-tree")
}

func RefExists(dir string, ref string) bool {
    return RunCheck(dir, "rev-parse", "--verify", ref)
}

func HasUncommittedChanges(dir string) (bool, error) {
    out, err := RunOutput(dir, "status", "--porcelain")
    if err != nil {
        return false, err
    }
    return len(out) > 0, nil
}

func UnpushedCommitCount(worktreePath, baseBranch string) (int, error) {
    out, err := RunOutput(worktreePath, "rev-list", "--count", "origin/"+baseBranch+"..HEAD")
    if err != nil {
        return 0, err
    }
    var n int
    fmt.Sscanf(out, "%d", &n)
    return n, nil
}

func RepoRootPath(dir string) (string, error) {
    return RunOutput(dir, "rev-parse", "--show-toplevel")
}
```

- [ ] **Step 3: Implement branch operations**

```go
// internal/git/branch.go
package git

import (
    "fmt"
    "strings"
)

func DiscoverDefaultBranch(repoPath string) (string, error) {
    for _, branch := range []string{"main", "master"} {
        if RefExists(repoPath, "origin/"+branch) {
            return branch, nil
        }
    }

    out, err := RunOutput(repoPath, "rev-parse", "--abbrev-ref", "origin/HEAD")
    if err == nil && out != "origin/HEAD" {
        return strings.TrimPrefix(out, "origin/"), nil
    }

    return "", fmt.Errorf("cannot determine default branch; use --base to specify one")
}

func CreateBranch(repoPath, branchName, fromRef string) error {
    _, err := RunOutput(repoPath, "branch", branchName, fromRef)
    return err
}

func DeleteBranch(repoPath, branchName string) error {
    _, err := RunOutput(repoPath, "branch", "-D", branchName)
    return err
}

func FetchOrigin(repoPath string) error {
    _, err := RunOutput(repoPath, "fetch", "origin")
    return err
}
```

- [ ] **Step 4: Implement worktree operations**

```go
// internal/git/worktree.go
package git

import (
    "errors"
    "fmt"
)

func CreateWorktree(repoPath, worktreePath, branchName string) error {
    _, err := RunOutput(repoPath, "worktree", "add", worktreePath, branchName)
    return err
}

func RemoveWorktree(repoPath, worktreePath string) error {
    _, err := RunOutput(repoPath, "worktree", "remove", "--force", worktreePath)
    return err
}

func SetupSession(repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
    if fetch {
        if err := FetchOrigin(repoPath); err != nil {
            return fmt.Errorf("fetch: %w", err)
        }
    }

    if err := CreateBranch(repoPath, branchName, "origin/"+baseBranch); err != nil {
        return fmt.Errorf("create branch: %w", err)
    }

    if err := CreateWorktree(repoPath, worktreePath, branchName); err != nil {
        _ = DeleteBranch(repoPath, branchName)
        return fmt.Errorf("create worktree: %w", err)
    }

    return nil
}

func TeardownSession(repoPath, worktreePath, branchName string) error {
    var errs []error

    if err := RemoveWorktree(repoPath, worktreePath); err != nil {
        errs = append(errs, fmt.Errorf("remove worktree: %w", err))
    }

    if err := DeleteBranch(repoPath, branchName); err != nil {
        errs = append(errs, fmt.Errorf("delete branch: %w", err))
    }

    return errors.Join(errs...)
}
```

- [ ] **Step 5: Implement username discovery**

```go
// internal/git/username.go
package git

import (
    "fmt"
    "net/url"
    "os/exec"
    "strings"
)

func DiscoverGitHubUsername(repoPath string) (string, error) {
    if u, err := ghCLIUsername(); err == nil && u != "" {
        return u, nil
    }

    if u, err := RunOutput(repoPath, "config", "github.user"); err == nil && u != "" {
        return u, nil
    }

    if remoteURL, err := RunOutput(repoPath, "remote", "get-url", "origin"); err == nil {
        if u, ok := ParseGitHubUsername(remoteURL); ok {
            return u, nil
        }
    }

    return "", fmt.Errorf("cannot determine GitHub username; set github_username in config")
}

func ghCLIUsername() (string, error) {
    if _, err := exec.LookPath("gh"); err != nil {
        return "", err
    }
    cmd := exec.Command("gh", "api", "user", "--jq", ".login")
    out, err := cmd.Output()
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(string(out)), nil
}

func ParseGitHubUsername(remoteURL string) (string, bool) {
    if strings.HasPrefix(remoteURL, "git@github.com:") {
        rest := strings.TrimPrefix(remoteURL, "git@github.com:")
        parts := strings.SplitN(rest, "/", 2)
        if len(parts) == 2 {
            return parts[0], true
        }
    }

    if u, err := url.Parse(remoteURL); err == nil && u.Host == "github.com" {
        parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
        if len(parts) == 2 {
            return parts[0], true
        }
    }

    return "", false
}
```

- [ ] **Step 6: Run all git tests**

Run: `go test ./internal/git/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "feat: add git operations for worktrees, branches, and username discovery"
```

---

## Milestone 3: PTY & Daemon

### Task 7: PTY session management

**Files:**
- Create: `internal/pty/session.go`
- Create: `internal/pty/scrollback.go`
- Create: `internal/pty/session_test.go`
- Create: `internal/pty/scrollback_test.go`

- [ ] **Step 1: Write scrollback tests**

```go
// internal/pty/scrollback_test.go
package pty

import (
    "os"
    "path/filepath"
    "testing"
)

func TestScrollbackWrite(t *testing.T) {
    path := filepath.Join(t.TempDir(), "scroll.log")
    sb, err := NewScrollback(path, 1024)
    if err != nil {
        t.Fatal(err)
    }
    defer sb.Close()

    sb.Write([]byte("hello world"))

    data, _ := os.ReadFile(path)
    if string(data) != "hello world" {
        t.Errorf("log = %q", data)
    }
}

func TestScrollbackTail(t *testing.T) {
    path := filepath.Join(t.TempDir(), "scroll.log")
    sb, err := NewScrollback(path, 1024)
    if err != nil {
        t.Fatal(err)
    }
    defer sb.Close()

    sb.Write([]byte("line1\nline2\nline3\n"))

    tail, err := sb.Tail(2)
    if err != nil {
        t.Fatal(err)
    }
    if string(tail) != "line2\nline3\n" {
        t.Errorf("tail = %q", tail)
    }
}
```

- [ ] **Step 2: Implement scrollback**

```go
// internal/pty/scrollback.go
package pty

import (
    "bytes"
    "fmt"
    "os"
    "sync"
)

type Scrollback struct {
    mu      sync.Mutex
    file    *os.File
    path    string
    maxSize int64
    written int64
}

func NewScrollback(path string, maxSize int64) (*Scrollback, error) {
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
    if err != nil {
        return nil, fmt.Errorf("open scrollback: %w", err)
    }

    info, _ := f.Stat()
    written := int64(0)
    if info != nil {
        written = info.Size()
    }

    return &Scrollback{
        file:    f,
        path:    path,
        maxSize: maxSize,
        written: written,
    }, nil
}

func (s *Scrollback) Write(data []byte) (int, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    n, err := s.file.Write(data)
    s.written += int64(n)
    return n, err
}

func (s *Scrollback) Tail(lines int) ([]byte, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    data, err := os.ReadFile(s.path)
    if err != nil {
        return nil, err
    }

    if lines <= 0 {
        return data, nil
    }

    idx := len(data)
    count := 0
    for idx > 0 && count < lines {
        idx--
        if data[idx] == '\n' && idx < len(data)-1 {
            count++
        }
    }
    if idx > 0 {
        idx++
    }

    return bytes.Clone(data[idx:]), nil
}

func (s *Scrollback) Close() error {
    return s.file.Close()
}

func (s *Scrollback) Remove() error {
    s.Close()
    return os.Remove(s.path)
}
```

- [ ] **Step 3: Implement PTY session**

```go
// internal/pty/session.go
package pty

import (
    "errors"
    "fmt"
    "io"
    "os"
    "os/exec"
    "sync"
    "syscall"

    "github.com/creack/pty"
)

type Session struct {
    ID         string
    Cmd        *exec.Cmd
    Ptmx       *os.File
    Scrollback *Scrollback

    mu             sync.RWMutex
    attachedWriter io.Writer
    done           chan struct{}
    exitCode       int
    exited         bool
}

type SessionOpts struct {
    ID          string
    Command     string
    Args        []string
    Dir         string
    Env         map[string]string
    Rows, Cols  uint16
    LogPath     string
    MaxLogSize  int64
}

func NewSession(opts SessionOpts) (*Session, error) {
    cmd := exec.Command(opts.Command, opts.Args...)
    cmd.Dir = opts.Dir
    cmd.Env = buildEnv(opts.Env)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        Setsid:  true,
        Setctty: true,
        Ctty:    0,
    }

    size := &pty.Winsize{Rows: opts.Rows, Cols: opts.Cols}
    ptmx, err := pty.StartWithSize(cmd, size)
    if err != nil {
        return nil, fmt.Errorf("start pty: %w", err)
    }

    sb, err := NewScrollback(opts.LogPath, opts.MaxLogSize)
    if err != nil {
        ptmx.Close()
        cmd.Process.Kill()
        return nil, fmt.Errorf("scrollback: %w", err)
    }

    s := &Session{
        ID:         opts.ID,
        Cmd:        cmd,
        Ptmx:       ptmx,
        Scrollback: sb,
        done:       make(chan struct{}),
    }

    go s.readLoop()
    go s.waitLoop()

    return s, nil
}

func (s *Session) readLoop() {
    buf := make([]byte, 32*1024)
    for {
        n, err := s.Ptmx.Read(buf)
        if n > 0 {
            chunk := buf[:n]
            s.Scrollback.Write(chunk)

            s.mu.RLock()
            w := s.attachedWriter
            s.mu.RUnlock()

            if w != nil {
                w.Write(chunk)
            }
        }
        if err != nil {
            return
        }
    }
}

func (s *Session) waitLoop() {
    err := s.Cmd.Wait()

    s.mu.Lock()
    s.exited = true
    if err != nil {
        var exitErr *exec.ExitError
        if errors.As(err, &exitErr) {
            s.exitCode = exitErr.ExitCode()
        } else {
            s.exitCode = -1
        }
    }
    s.mu.Unlock()

    close(s.done)
}

func (s *Session) WriteInput(data []byte) error {
    _, err := s.Ptmx.Write(data)
    return err
}

func (s *Session) Resize(rows, cols uint16) error {
    return pty.Setsize(s.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *Session) Attach(w io.Writer) {
    s.mu.Lock()
    s.attachedWriter = w
    s.mu.Unlock()
}

func (s *Session) Detach() {
    s.mu.Lock()
    s.attachedWriter = nil
    s.mu.Unlock()
}

func (s *Session) Done() <-chan struct{} {
    return s.done
}

func (s *Session) Exited() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.exited
}

func (s *Session) ExitCode() int {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.exitCode
}

func (s *Session) Kill() error {
    if s.Cmd.Process == nil {
        return nil
    }
    return syscall.Kill(-s.Cmd.Process.Pid, syscall.SIGTERM)
}

func (s *Session) ForceKill() error {
    if s.Cmd.Process == nil {
        return nil
    }
    return syscall.Kill(-s.Cmd.Process.Pid, syscall.SIGKILL)
}

func (s *Session) Close() {
    s.Ptmx.Close()
    s.Scrollback.Close()
}

func buildEnv(extra map[string]string) []string {
    env := os.Environ()
    env = append(env, "TERM=xterm-256color")
    for k, v := range extra {
        env = append(env, k+"="+v)
    }
    return env
}
```

- [ ] **Step 4: Write PTY session test (spawns a real process)**

```go
// internal/pty/session_test.go
package pty

import (
    "bytes"
    "path/filepath"
    "testing"
    "time"
)

func TestSessionEcho(t *testing.T) {
    logPath := filepath.Join(t.TempDir(), "test.log")

    s, err := NewSession(SessionOpts{
        ID:         "test",
        Command:    "echo",
        Args:       []string{"hello graith"},
        Dir:        t.TempDir(),
        Rows:       24,
        Cols:       80,
        LogPath:    logPath,
        MaxLogSize: 1024 * 1024,
    })
    if err != nil {
        t.Fatal(err)
    }
    defer s.Close()

    select {
    case <-s.Done():
    case <-time.After(5 * time.Second):
        t.Fatal("timeout waiting for process exit")
    }

    if !s.Exited() {
        t.Error("expected process to have exited")
    }
    if s.ExitCode() != 0 {
        t.Errorf("exit code = %d, want 0", s.ExitCode())
    }

    tail, err := s.Scrollback.Tail(0)
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Contains(tail, []byte("hello graith")) {
        t.Errorf("scrollback = %q, want to contain 'hello graith'", tail)
    }
}

func TestSessionAttachDetach(t *testing.T) {
    logPath := filepath.Join(t.TempDir(), "test.log")

    s, err := NewSession(SessionOpts{
        ID:         "test",
        Command:    "echo",
        Args:       []string{"attached output"},
        Dir:        t.TempDir(),
        Rows:       24,
        Cols:       80,
        LogPath:    logPath,
        MaxLogSize: 1024 * 1024,
    })
    if err != nil {
        t.Fatal(err)
    }
    defer s.Close()

    var buf bytes.Buffer
    s.Attach(&buf)

    select {
    case <-s.Done():
    case <-time.After(5 * time.Second):
        t.Fatal("timeout")
    }

    time.Sleep(100 * time.Millisecond)

    s.Detach()

    if !bytes.Contains(buf.Bytes(), []byte("attached output")) {
        t.Errorf("attached output = %q", buf.String())
    }
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/pty/ -v -timeout 30s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: add PTY session management and scrollback logging"
```

---

### Task 8: Daemon state persistence

**Files:**
- Create: `internal/daemon/state.go`
- Create: `internal/daemon/state_test.go`

- [ ] **Step 1: Write state tests**

```go
// internal/daemon/state_test.go
package daemon

import (
    "path/filepath"
    "testing"
    "time"
)

func TestStateSaveLoad(t *testing.T) {
    path := filepath.Join(t.TempDir(), "state.json")

    state := &State{
        Sessions: map[string]*SessionState{
            "abc123": {
                ID:           "abc123",
                Name:         "fix-bug",
                RepoPath:     "/home/user/repo",
                RepoName:     "repo",
                WorktreePath: "/home/user/.local/share/graith/worktrees/abc123",
                Branch:       "d0ugal/graith/fix-bug-abc123",
                BaseBranch:   "main",
                Agent:        "claude",
                Status:       StatusRunning,
                CreatedAt:    time.Now().UTC(),
            },
        },
    }

    if err := SaveState(path, state); err != nil {
        t.Fatal(err)
    }

    loaded, err := LoadState(path)
    if err != nil {
        t.Fatal(err)
    }

    s, ok := loaded.Sessions["abc123"]
    if !ok {
        t.Fatal("session not found after load")
    }
    if s.Name != "fix-bug" || s.Agent != "claude" || s.Status != StatusRunning {
        t.Errorf("session = %+v", s)
    }
}

func TestLoadStateMissing(t *testing.T) {
    state, err := LoadState("/nonexistent/state.json")
    if err != nil {
        t.Fatal(err)
    }
    if len(state.Sessions) != 0 {
        t.Error("expected empty state for missing file")
    }
}

func TestLoadStateCorrupted(t *testing.T) {
    path := filepath.Join(t.TempDir(), "state.json")
    writeTestFile(t, path, "not json")

    state, err := LoadState(path)
    if err != nil {
        t.Fatal(err)
    }
    if len(state.Sessions) != 0 {
        t.Error("expected empty state for corrupted file")
    }
}

func writeTestFile(t *testing.T, path, content string) {
    t.Helper()
    if err := writeFileAtomic(path, []byte(content)); err != nil {
        t.Fatal(err)
    }
}
```

- [ ] **Step 2: Implement state persistence**

```go
// internal/daemon/state.go
package daemon

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "time"
)

type SessionStatus string

const (
    StatusRunning SessionStatus = "running"
    StatusStopped SessionStatus = "stopped"
    StatusErrored SessionStatus = "errored"
)

type SessionState struct {
    ID              string        `json:"id"`
    Name            string        `json:"name"`
    RepoPath        string        `json:"repo_path"`
    RepoName        string        `json:"repo_name"`
    WorktreePath    string        `json:"worktree_path"`
    Branch          string        `json:"branch"`
    BaseBranch      string        `json:"base_branch"`
    Agent           string        `json:"agent"`
    AgentSessionID  string        `json:"agent_session_id,omitempty"`
    Status          SessionStatus `json:"status"`
    ExitCode        *int          `json:"exit_code,omitempty"`
    PID             int           `json:"pid,omitempty"`
    CreatedAt       time.Time     `json:"created_at"`
    LastAttachedAt  *time.Time    `json:"last_attached_at,omitempty"`
    AttachedClient  string        `json:"attached_client,omitempty"`
}

type State struct {
    Sessions map[string]*SessionState `json:"sessions"`
}

func NewState() *State {
    return &State{Sessions: make(map[string]*SessionState)}
}

func LoadState(path string) (*State, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return NewState(), nil
        }
        return nil, fmt.Errorf("read state: %w", err)
    }

    var state State
    if err := json.Unmarshal(data, &state); err != nil {
        slog.Warn("corrupted state file, starting fresh", "path", path, "err", err)
        return NewState(), nil
    }

    if state.Sessions == nil {
        state.Sessions = make(map[string]*SessionState)
    }

    return &state, nil
}

func SaveState(path string, state *State) error {
    data, err := json.MarshalIndent(state, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal state: %w", err)
    }
    return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
    dir := filepath.Dir(path)
    tmp, err := os.CreateTemp(dir, ".state-*.tmp")
    if err != nil {
        return fmt.Errorf("create temp: %w", err)
    }
    tmpPath := tmp.Name()

    if _, err := tmp.Write(data); err != nil {
        tmp.Close()
        os.Remove(tmpPath)
        return fmt.Errorf("write temp: %w", err)
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmpPath)
        return err
    }
    if err := os.Rename(tmpPath, path); err != nil {
        os.Remove(tmpPath)
        return fmt.Errorf("rename: %w", err)
    }
    return nil
}

func (s *State) Reconcile() {
    for id, sess := range s.Sessions {
        if sess.Status == StatusRunning && sess.PID > 0 {
            if !isProcessAlive(sess.PID) {
                slog.Info("session process died, marking stopped", "id", id, "pid", sess.PID)
                sess.Status = StatusStopped
                sess.PID = 0
            }
        }
        if sess.WorktreePath != "" {
            if _, err := os.Stat(sess.WorktreePath); os.IsNotExist(err) {
                slog.Warn("worktree missing for session", "id", id, "path", sess.WorktreePath)
            }
        }
    }
}

func isProcessAlive(pid int) bool {
    proc, err := os.FindProcess(pid)
    if err != nil {
        return false
    }
    err = proc.Signal(os.Signal(nil))
    return err == nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add daemon state persistence with atomic writes and reconciliation"
```

---

### Task 9: Daemon socket server

**Files:**
- Create: `internal/daemon/pid.go`
- Create: `internal/daemon/server.go`
- Create: `internal/daemon/daemon.go`
- Create: `internal/daemon/pid_test.go`
- Create: `internal/daemon/server_test.go`

- [ ] **Step 1: Write PID file tests**

```go
// internal/daemon/pid_test.go
package daemon

import (
    "os"
    "path/filepath"
    "strconv"
    "testing"
)

func TestAcquirePIDFile(t *testing.T) {
    path := filepath.Join(t.TempDir(), "test.pid")

    if err := AcquirePIDFile(path); err != nil {
        t.Fatal(err)
    }
    defer ReleasePIDFile(path)

    data, _ := os.ReadFile(path)
    pid, _ := strconv.Atoi(string(data[:len(data)-1]))
    if pid != os.Getpid() {
        t.Errorf("pid = %d, want %d", pid, os.Getpid())
    }
}

func TestAcquirePIDFileStale(t *testing.T) {
    path := filepath.Join(t.TempDir(), "test.pid")
    os.WriteFile(path, []byte("999999\n"), 0o600)

    if err := AcquirePIDFile(path); err != nil {
        t.Fatalf("should succeed with stale PID: %v", err)
    }
    defer ReleasePIDFile(path)
}

func TestAcquirePIDFileLive(t *testing.T) {
    path := filepath.Join(t.TempDir(), "test.pid")
    os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)

    err := AcquirePIDFile(path)
    if err == nil {
        t.Error("should fail when PID is live")
    }
}
```

- [ ] **Step 2: Implement PID file management**

```go
// internal/daemon/pid.go
package daemon

import (
    "errors"
    "fmt"
    "os"
    "strconv"
    "strings"
    "syscall"
)

var ErrDaemonRunning = errors.New("daemon already running")

func AcquirePIDFile(path string) error {
    if data, err := os.ReadFile(path); err == nil {
        pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
        if err == nil && isPIDAlive(pid) {
            return fmt.Errorf("%w (pid %d)", ErrDaemonRunning, pid)
        }
        _ = os.Remove(path)
    }

    f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
    if err != nil {
        if os.IsExist(err) {
            return fmt.Errorf("%w: concurrent start detected", ErrDaemonRunning)
        }
        return fmt.Errorf("create pid file: %w", err)
    }
    defer f.Close()

    _, err = fmt.Fprintf(f, "%d\n", os.Getpid())
    return err
}

func ReleasePIDFile(path string) {
    _ = os.Remove(path)
}

func isPIDAlive(pid int) bool {
    proc, err := os.FindProcess(pid)
    if err != nil {
        return false
    }
    return proc.Signal(syscall.Signal(0)) == nil
}
```

- [ ] **Step 3: Implement socket server**

```go
// internal/daemon/server.go
package daemon

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "net"
    "os"
    "path/filepath"
    "sync"
)

type Server struct {
    listener net.Listener
    handler  func(ctx context.Context, conn net.Conn)
    wg       sync.WaitGroup
    log      *slog.Logger
}

func Listen(sockPath string) (net.Listener, error) {
    dir := filepath.Dir(sockPath)
    if err := os.MkdirAll(dir, 0o700); err != nil {
        return nil, fmt.Errorf("create socket dir: %w", err)
    }
    _ = os.Remove(sockPath)

    l, err := net.Listen("unix", sockPath)
    if err != nil {
        return nil, fmt.Errorf("listen: %w", err)
    }

    if err := os.Chmod(sockPath, 0o700); err != nil {
        l.Close()
        return nil, fmt.Errorf("chmod socket: %w", err)
    }

    return l, nil
}

func NewServer(l net.Listener, handler func(ctx context.Context, conn net.Conn), log *slog.Logger) *Server {
    return &Server{listener: l, handler: handler, log: log}
}

func (s *Server) Serve(ctx context.Context) error {
    for {
        conn, err := s.listener.Accept()
        if err != nil {
            if errors.Is(err, net.ErrClosed) {
                return nil
            }
            s.log.Warn("accept error", "err", err)
            continue
        }

        s.wg.Add(1)
        go func(c net.Conn) {
            defer s.wg.Done()
            defer c.Close()
            s.handler(ctx, c)
        }(conn)
    }
}

func (s *Server) Shutdown() {
    s.listener.Close()
    s.wg.Wait()
}
```

- [ ] **Step 4: Write server test**

```go
// internal/daemon/server_test.go
package daemon

import (
    "context"
    "net"
    "path/filepath"
    "sync/atomic"
    "testing"
    "time"
)

func TestServerAcceptsConnections(t *testing.T) {
    sockPath := filepath.Join(t.TempDir(), "test.sock")

    l, err := Listen(sockPath)
    if err != nil {
        t.Fatal(err)
    }

    var count atomic.Int32
    handler := func(ctx context.Context, conn net.Conn) {
        count.Add(1)
        buf := make([]byte, 16)
        conn.Read(buf)
    }

    srv := NewServer(l, handler, nil)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go srv.Serve(ctx)

    conn1, err := net.Dial("unix", sockPath)
    if err != nil {
        t.Fatal(err)
    }
    conn1.Write([]byte("hi"))
    conn1.Close()

    conn2, err := net.Dial("unix", sockPath)
    if err != nil {
        t.Fatal(err)
    }
    conn2.Write([]byte("hi"))
    conn2.Close()

    time.Sleep(100 * time.Millisecond)
    srv.Shutdown()

    if count.Load() != 2 {
        t.Errorf("handled %d connections, want 2", count.Load())
    }
}
```

- [ ] **Step 5: Run daemon tests**

Run: `go test ./internal/daemon/ -v -timeout 30s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: add daemon socket server, PID file management"
```

---

### Task 10: Daemon main loop

**Files:**
- Create: `internal/daemon/daemon.go`
- Create: `internal/daemon/handler.go`

- [ ] **Step 1: Implement the session manager**

```go
// internal/daemon/daemon.go
package daemon

import (
    "context"
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "path/filepath"
    "sync"
    "syscall"
    "time"

    "github.com/dougalmatthews/graith/internal/config"
    grpty "github.com/dougalmatthews/graith/internal/pty"
    "github.com/dougalmatthews/graith/internal/git"
)

type SessionManager struct {
    mu       sync.RWMutex
    state    *State
    sessions map[string]*grpty.Session
    cfg      *config.Config
    paths    config.Paths
    log      *slog.Logger
}

func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
    return &SessionManager{
        state:    NewState(),
        sessions: make(map[string]*grpty.Session),
        cfg:      cfg,
        paths:    paths,
        log:      log,
    }
}

func (sm *SessionManager) LoadState() error {
    state, err := LoadState(sm.paths.StateFile)
    if err != nil {
        return err
    }
    state.Reconcile()
    sm.state = state
    return sm.saveState()
}

func (sm *SessionManager) saveState() error {
    return SaveState(sm.paths.StateFile, sm.state)
}

func generateID() string {
    b := make([]byte, 4)
    rand.Read(b)
    return hex.EncodeToString(b)
}

func repoHash(repoPath string) string {
    b := make([]byte, 8)
    // Simple hash of path for directory naming
    h := uint64(0)
    for _, c := range repoPath {
        h = h*31 + uint64(c)
    }
    for i := 0; i < 8; i++ {
        b[i] = byte(h >> (i * 8))
    }
    return hex.EncodeToString(b)[:12]
}

func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch string, rows, cols uint16) (*SessionState, error) {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if !git.IsInsideGitRepo(repoPath) {
        return nil, fmt.Errorf("not inside a git repository: %s", repoPath)
    }

    repoRoot, err := git.RepoRootPath(repoPath)
    if err != nil {
        return nil, fmt.Errorf("find repo root: %w", err)
    }

    if baseBranch == "" {
        baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
        if err != nil {
            return nil, err
        }
    }

    agent, ok := sm.cfg.Agents[agentName]
    if !ok {
        return nil, fmt.Errorf("unknown agent %q", agentName)
    }

    id := generateID()
    repoName := filepath.Base(repoRoot)

    username := sm.cfg.GitHubUsername
    if username == "" {
        username, _ = git.DiscoverGitHubUsername(repoRoot)
    }

    branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
    branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

    worktreePath := filepath.Join(sm.paths.DataDir, "worktrees", repoHash(repoRoot), id)

    if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
        return nil, fmt.Errorf("setup git session: %w", err)
    }

    agentSessionID := ""
    if agentName == "claude" {
        b := make([]byte, 16)
        rand.Read(b)
        agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
    }

    vars := config.TemplateVars{
        Username:       username,
        AgentSessionID: agentSessionID,
        SessionName:    name,
        SessionID:      id,
        WorktreePath:   worktreePath,
    }
    expandedArgs, err := config.ExpandSlice(agent.Args, vars)
    if err != nil {
        git.TeardownSession(repoRoot, worktreePath, branchName)
        return nil, fmt.Errorf("expand agent args: %w", err)
    }

    logPath := filepath.Join(sm.paths.LogDir, id+".log")

    ptySess, err := grpty.NewSession(grpty.SessionOpts{
        ID:         id,
        Command:    agent.Command,
        Args:       expandedArgs,
        Dir:        worktreePath,
        Env:        agent.Env,
        Rows:       rows,
        Cols:       cols,
        LogPath:    logPath,
        MaxLogSize: 100 * 1024 * 1024,
    })
    if err != nil {
        git.TeardownSession(repoRoot, worktreePath, branchName)
        return nil, fmt.Errorf("start pty session: %w", err)
    }

    sessState := &SessionState{
        ID:             id,
        Name:           name,
        RepoPath:       repoRoot,
        RepoName:       repoName,
        WorktreePath:   worktreePath,
        Branch:         branchName,
        BaseBranch:     baseBranch,
        Agent:          agentName,
        AgentSessionID: agentSessionID,
        Status:         StatusRunning,
        PID:            ptySess.Cmd.Process.Pid,
        CreatedAt:      time.Now().UTC(),
    }

    sm.state.Sessions[id] = sessState
    sm.sessions[id] = ptySess

    go sm.watchSession(id, ptySess)

    if err := sm.saveState(); err != nil {
        sm.log.Error("failed to save state", "err", err)
    }

    return sessState, nil
}

func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
    <-sess.Done()

    sm.mu.Lock()
    defer sm.mu.Unlock()

    if s, ok := sm.state.Sessions[id]; ok {
        exitCode := sess.ExitCode()
        s.Status = StatusStopped
        s.ExitCode = &exitCode
        s.PID = 0
        sm.saveState()
    }

    sm.log.Info("session exited", "id", id, "exit_code", sess.ExitCode())
}

func (sm *SessionManager) Delete(id string) error {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    sessState, ok := sm.state.Sessions[id]
    if !ok {
        return fmt.Errorf("session %q not found", id)
    }

    if ptySess, ok := sm.sessions[id]; ok {
        if !ptySess.Exited() {
            ptySess.Kill()
            select {
            case <-ptySess.Done():
            case <-time.After(5 * time.Second):
                ptySess.ForceKill()
            }
        }
        ptySess.Close()
        delete(sm.sessions, id)
    }

    git.TeardownSession(sessState.RepoPath, sessState.WorktreePath, sessState.Branch)
    os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))

    delete(sm.state.Sessions, id)
    return sm.saveState()
}

func (sm *SessionManager) Rename(id, newName string) error {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    s, ok := sm.state.Sessions[id]
    if !ok {
        return fmt.Errorf("session %q not found", id)
    }
    s.Name = newName
    return sm.saveState()
}

func (sm *SessionManager) List() []*SessionState {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    list := make([]*SessionState, 0, len(sm.state.Sessions))
    for _, s := range sm.state.Sessions {
        list = append(list, s)
    }
    return list
}

func (sm *SessionManager) Get(id string) (*SessionState, bool) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()
    s, ok := sm.state.Sessions[id]
    return s, ok
}

func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()
    s, ok := sm.sessions[id]
    return s, ok
}

func (sm *SessionManager) FindByName(name string) (*SessionState, bool) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    for _, s := range sm.state.Sessions {
        if s.Name == name {
            return s, true
        }
    }

    for _, s := range sm.state.Sessions {
        if len(name) > 0 && len(s.Name) >= len(name) && s.Name[:len(name)] == name {
            return s, true
        }
    }

    return nil, false
}

func (sm *SessionManager) StopAll(ctx context.Context) {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    for id, sess := range sm.sessions {
        if !sess.Exited() {
            sm.log.Info("stopping session", "id", id)
            sess.Kill()
        }
    }

    deadline := time.After(5 * time.Second)
    for id, sess := range sm.sessions {
        select {
        case <-sess.Done():
        case <-deadline:
            sm.log.Warn("force killing session", "id", id)
            sess.ForceKill()
        }
    }
}

func Run(cfg *config.Config, paths config.Paths) error {
    log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

    if err := paths.EnsureDirs(); err != nil {
        return err
    }

    if err := AcquirePIDFile(paths.PIDFile); err != nil {
        return err
    }
    defer ReleasePIDFile(paths.PIDFile)

    l, err := Listen(paths.SocketPath)
    if err != nil {
        return err
    }
    defer os.Remove(paths.SocketPath)
    defer l.Close()

    sm := NewSessionManager(cfg, paths, log)
    if err := sm.LoadState(); err != nil {
        log.Warn("failed to load state", "err", err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
        HandleConnection(ctx, conn, sm, log)
    }, log)

    go srv.Serve(ctx)

    log.Info("daemon started", "socket", paths.SocketPath, "pid", os.Getpid())

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
    <-sigCh

    log.Info("shutting down")
    cancel()
    sm.StopAll(ctx)
    srv.Shutdown()

    return nil
}
```

- [ ] **Step 2: Implement connection handler**

```go
// internal/daemon/handler.go
package daemon

import (
    "context"
    "io"
    "log/slog"
    "net"
    "time"

    "github.com/dougalmatthews/graith/internal/protocol"
)

const protocolVersion = "1.0"

func HandleConnection(ctx context.Context, conn net.Conn, sm *SessionManager, log *slog.Logger) {
    reader := protocol.NewFrameReader(conn)
    writer := protocol.NewFrameWriter(conn)

    var attachedSessionID string

    sendControl := func(msgType string, payload any) {
        data, err := protocol.EncodeControl(msgType, payload)
        if err != nil {
            log.Error("encode control", "err", err)
            return
        }
        writer.WriteFrame(protocol.ChannelControl, data)
    }

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        frame, err := reader.ReadFrame()
        if err != nil {
            if err != io.EOF {
                log.Debug("client read error", "err", err)
            }
            if attachedSessionID != "" {
                if pty, ok := sm.GetPTY(attachedSessionID); ok {
                    pty.Detach()
                }
            }
            return
        }

        switch frame.Channel {
        case protocol.ChannelControl:
            msg, err := protocol.DecodeControl(frame.Payload)
            if err != nil {
                sendControl("error", protocol.ErrorMsg{Message: "malformed message"})
                continue
            }

            switch msg.Type {
            case "handshake":
                var h protocol.HandshakeMsg
                protocol.DecodePayload(msg, &h)
                sendControl("handshake_ok", protocol.HandshakeOkMsg{Version: protocolVersion})
                log.Info("client connected", "client_id", h.ClientID, "cwd", h.Cwd)

            case "list":
                sessions := sm.List()
                infos := make([]protocol.SessionInfo, 0, len(sessions))
                for _, s := range sessions {
                    infos = append(infos, toSessionInfo(s))
                }
                sendControl("session_list", protocol.SessionListMsg{Sessions: infos})

            case "create":
                var c protocol.CreateMsg
                protocol.DecodePayload(msg, &c)
                agentName := c.Agent
                if agentName == "" {
                    agentName = sm.cfg.DefaultAgent
                }
                sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, 24, 80)
                if err != nil {
                    sendControl("error", protocol.ErrorMsg{Message: err.Error()})
                } else {
                    sendControl("created", toSessionInfo(sess))
                }

            case "attach":
                var a protocol.AttachMsg
                protocol.DecodePayload(msg, &a)

                if attachedSessionID != "" {
                    if pty, ok := sm.GetPTY(attachedSessionID); ok {
                        pty.Detach()
                    }
                }

                ptySess, ok := sm.GetPTY(a.SessionID)
                if !ok {
                    sendControl("error", protocol.ErrorMsg{Message: "session not found"})
                    continue
                }

                dataWriter := &frameDataWriter{writer: writer}
                ptySess.Attach(dataWriter)
                attachedSessionID = a.SessionID

                now := time.Now().UTC()
                sm.mu.Lock()
                if s, ok := sm.state.Sessions[a.SessionID]; ok {
                    s.LastAttachedAt = &now
                }
                sm.mu.Unlock()

                sess, _ := sm.Get(a.SessionID)
                sendControl("attached", toSessionInfo(sess))

            case "detach":
                if attachedSessionID != "" {
                    if pty, ok := sm.GetPTY(attachedSessionID); ok {
                        pty.Detach()
                    }
                    attachedSessionID = ""
                }
                sendControl("detached", protocol.DetachedMsg{Reason: "user"})

            case "delete":
                var d protocol.DeleteMsg
                protocol.DecodePayload(msg, &d)
                if err := sm.Delete(d.SessionID); err != nil {
                    sendControl("error", protocol.ErrorMsg{Message: err.Error()})
                } else {
                    sendControl("deleted", struct{ SessionID string `json:"session_id"` }{d.SessionID})
                }

            case "rename":
                var r protocol.RenameMsg
                protocol.DecodePayload(msg, &r)
                if err := sm.Rename(r.SessionID, r.NewName); err != nil {
                    sendControl("error", protocol.ErrorMsg{Message: err.Error()})
                } else {
                    sendControl("renamed", struct {
                        SessionID string `json:"session_id"`
                        NewName   string `json:"new_name"`
                    }{r.SessionID, r.NewName})
                }

            case "resize":
                var r protocol.ResizeMsg
                protocol.DecodePayload(msg, &r)
                if attachedSessionID != "" {
                    if pty, ok := sm.GetPTY(attachedSessionID); ok {
                        pty.Resize(r.Rows, r.Cols)
                    }
                }
            }

        case protocol.ChannelData:
            if attachedSessionID != "" {
                if pty, ok := sm.GetPTY(attachedSessionID); ok {
                    pty.WriteInput(frame.Payload)
                }
            }
        }
    }
}

type frameDataWriter struct {
    writer *protocol.FrameWriter
}

func (w *frameDataWriter) Write(p []byte) (int, error) {
    if err := w.writer.WriteFrame(protocol.ChannelData, p); err != nil {
        return 0, err
    }
    return len(p), nil
}

func toSessionInfo(s *SessionState) protocol.SessionInfo {
    return protocol.SessionInfo{
        ID:             s.ID,
        Name:           s.Name,
        RepoPath:       s.RepoPath,
        RepoName:       s.RepoName,
        WorktreePath:   s.WorktreePath,
        Branch:         s.Branch,
        Agent:          s.Agent,
        AgentSessionID: s.AgentSessionID,
        Status:         string(s.Status),
        ExitCode:       s.ExitCode,
        CreatedAt:      s.CreatedAt.Format(time.RFC3339),
    }
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: compiles without errors

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add daemon session manager and connection handler"
```

---

## Milestone 4: Client & CLI

### Task 11: Client connection and passthrough

**Files:**
- Create: `internal/client/client.go`
- Create: `internal/client/passthrough.go`
- Create: `internal/client/autostart.go`

- [ ] **Step 1: Implement daemon auto-start**

```go
// internal/client/autostart.go
package client

import (
    "context"
    "fmt"
    "net"
    "os"
    "os/exec"
    "syscall"
    "time"
)

func EnsureDaemon(sockPath string) (net.Conn, error) {
    if conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond); err == nil {
        return conn, nil
    }

    if err := startDaemon(); err != nil {
        return nil, fmt.Errorf("start daemon: %w", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    for {
        if conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond); err == nil {
            return conn, nil
        }
        select {
        case <-ctx.Done():
            return nil, fmt.Errorf("daemon did not start in time")
        case <-time.After(50 * time.Millisecond):
        }
    }
}

func startDaemon() error {
    self, err := os.Executable()
    if err != nil {
        return err
    }

    devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
    if err != nil {
        return err
    }
    defer devNull.Close()

    cmd := exec.Command(self, "daemon", "start")
    cmd.Stdin = devNull
    cmd.Stdout = devNull
    cmd.Stderr = devNull
    cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

    return cmd.Start()
}
```

- [ ] **Step 2: Implement client connection**

```go
// internal/client/client.go
package client

import (
    "fmt"
    "net"
    "os"

    "github.com/dougalmatthews/graith/internal/config"
    "github.com/dougalmatthews/graith/internal/protocol"
    "golang.org/x/term"
)

type Client struct {
    conn   net.Conn
    reader *protocol.FrameReader
    writer *protocol.FrameWriter
    cfg    *config.Config
    paths  config.Paths
}

func New(cfg *config.Config, paths config.Paths) (*Client, error) {
    conn, err := EnsureDaemon(paths.SocketPath)
    if err != nil {
        return nil, err
    }

    c := &Client{
        conn:   conn,
        reader: protocol.NewFrameReader(conn),
        writer: protocol.NewFrameWriter(conn),
        cfg:    cfg,
        paths:  paths,
    }

    return c, nil
}

func (c *Client) Close() {
    c.conn.Close()
}

func (c *Client) Handshake() error {
    cwd, _ := os.Getwd()
    cols, rows := 80, 24
    if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
        cols, rows = w, h
    }

    return c.SendControl("handshake", protocol.HandshakeMsg{
        Version:      "1.0",
        ClientID:     fmt.Sprintf("%d", os.Getpid()),
        TerminalSize: [2]uint16{uint16(cols), uint16(rows)},
        Cwd:          cwd,
    })
}

func (c *Client) SendControl(msgType string, payload any) error {
    data, err := protocol.EncodeControl(msgType, payload)
    if err != nil {
        return err
    }
    return c.writer.WriteFrame(protocol.ChannelControl, data)
}

func (c *Client) SendData(data []byte) error {
    return c.writer.WriteFrame(protocol.ChannelData, data)
}

func (c *Client) ReadFrame() (protocol.Frame, error) {
    return c.reader.ReadFrame()
}

func (c *Client) ReadControlResponse() (protocol.Envelope, error) {
    frame, err := c.reader.ReadFrame()
    if err != nil {
        return protocol.Envelope{}, err
    }
    if frame.Channel != protocol.ChannelControl {
        return protocol.Envelope{}, fmt.Errorf("expected control frame, got channel %d", frame.Channel)
    }
    return protocol.DecodeControl(frame.Payload)
}
```

- [ ] **Step 3: Implement passthrough mode**

```go
// internal/client/passthrough.go
package client

import (
    "context"
    "io"
    "os"
    "os/signal"
    "syscall"

    "github.com/dougalmatthews/graith/internal/protocol"
    "golang.org/x/term"
)

type PassthroughResult int

const (
    ResultDetached PassthroughResult = iota
    ResultOverlay
    ResultShell
    ResultQuit
)

func (c *Client) RunPassthrough(ctx context.Context, prefixByte byte) PassthroughResult {
    fd := int(os.Stdin.Fd())
    oldState, err := term.MakeRaw(fd)
    if err != nil {
        return ResultQuit
    }
    defer term.Restore(fd, oldState)

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGWINCH)
    defer signal.Stop(sigCh)

    innerCtx, cancel := context.WithCancel(ctx)
    defer cancel()

    result := ResultQuit

    go func() {
        for {
            select {
            case <-innerCtx.Done():
                return
            case <-sigCh:
                if w, h, err := term.GetSize(fd); err == nil {
                    c.SendControl("resize", protocol.ResizeMsg{
                        Cols: uint16(w),
                        Rows: uint16(h),
                    })
                }
            }
        }
    }()

    go func() {
        defer cancel()
        for {
            frame, err := c.ReadFrame()
            if err != nil {
                return
            }
            select {
            case <-innerCtx.Done():
                return
            default:
            }

            switch frame.Channel {
            case protocol.ChannelData:
                os.Stdout.Write(frame.Payload)
            case protocol.ChannelControl:
                msg, _ := protocol.DecodeControl(frame.Payload)
                if msg.Type == "detached" {
                    result = ResultDetached
                    return
                }
            }
        }
    }()

    go func() {
        defer cancel()
        buf := make([]byte, 4096)
        for {
            n, err := os.Stdin.Read(buf)
            if err != nil {
                return
            }
            select {
            case <-innerCtx.Done():
                return
            default:
            }

            for i := 0; i < n; i++ {
                if buf[i] == prefixByte {
                    if i+1 < n {
                        next := buf[i+1]
                        if next == prefixByte {
                            c.SendData([]byte{prefixByte})
                            i++
                            continue
                        }
                        if next == 'd' {
                            result = ResultDetached
                            return
                        }
                        if next == 'w' || next == 0 {
                            result = ResultOverlay
                            return
                        }
                        if next == 's' {
                            result = ResultShell
                            return
                        }
                    } else {
                        result = ResultOverlay
                        return
                    }
                    continue
                }
            }

            c.SendData(buf[:n])
        }
    }()

    <-innerCtx.Done()

    term.Restore(fd, oldState)
    return result
}
```

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: compiles

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: add client connection, autostart, and passthrough mode"
```

---

### Task 12: Overlay UI

**Files:**
- Create: `internal/client/overlay.go`

- [ ] **Step 1: Implement the bubbletea overlay**

```go
// internal/client/overlay.go
package client

import (
    "fmt"
    "sort"
    "strings"

    "github.com/charmbracelet/bubbles/key"
    "github.com/charmbracelet/bubbles/list"
    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"
    "github.com/dougalmatthews/graith/internal/protocol"
)

type overlayState int

const (
    stateList overlayState = iota
    stateFilter
    stateConfirmDelete
)

type sessionItem struct {
    info protocol.SessionInfo
}

func (s sessionItem) Title() string {
    indicator := "●"
    color := lipgloss.Color("#00ff87")
    switch s.info.Status {
    case "stopped":
        indicator = "○"
        color = lipgloss.Color("#626262")
    case "errored":
        indicator = "✗"
        color = lipgloss.Color("#ff5f5f")
    }
    styled := lipgloss.NewStyle().Foreground(color).Render(indicator)
    return fmt.Sprintf("%s %s", styled, s.info.Name)
}

func (s sessionItem) Description() string {
    return fmt.Sprintf("%s  %s", s.info.Agent, s.info.Status)
}

func (s sessionItem) FilterValue() string {
    return s.info.Name + " " + s.info.RepoName
}

type groupHeader struct {
    name string
}

func (g groupHeader) Title() string {
    return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7B61FF")).Render(g.name)
}
func (g groupHeader) Description() string { return "" }
func (g groupHeader) FilterValue() string { return "" }

type overlayModel struct {
    list        list.Model
    filterInput textinput.Model
    state       overlayState
    selected    *protocol.SessionInfo
    width       int
    height      int
}

type OverlayResult struct {
    Action    string
    SessionID string
}

func buildGroupedItems(sessions []protocol.SessionInfo) []list.Item {
    groups := map[string][]protocol.SessionInfo{}
    var repoOrder []string
    seen := map[string]bool{}

    for _, s := range sessions {
        if !seen[s.RepoName] {
            repoOrder = append(repoOrder, s.RepoName)
            seen[s.RepoName] = true
        }
        groups[s.RepoName] = append(groups[s.RepoName], s)
    }
    sort.Strings(repoOrder)

    var items []list.Item
    for _, repo := range repoOrder {
        items = append(items, groupHeader{name: repo})
        for _, s := range groups[repo] {
            items = append(items, sessionItem{info: s})
        }
    }
    return items
}

func newOverlayModel(sessions []protocol.SessionInfo) overlayModel {
    items := buildGroupedItems(sessions)

    delegate := list.NewDefaultDelegate()
    delegate.ShowDescription = true

    l := list.New(items, delegate, 60, 20)
    l.Title = "Sessions"
    l.SetShowHelp(false)
    l.SetShowStatusBar(false)
    l.SetFilteringEnabled(false)
    l.KeyMap.Quit = key.NewBinding(key.WithKeys())

    fi := textinput.New()
    fi.Placeholder = "filter..."
    fi.CharLimit = 64

    return overlayModel{
        list:        l,
        filterInput: fi,
        state:       stateList,
    }
}

func (m overlayModel) Init() tea.Cmd {
    return nil
}

func (m overlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        m.list.SetSize(msg.Width, msg.Height-3)
        return m, nil

    case tea.KeyMsg:
        switch m.state {
        case stateFilter:
            switch msg.String() {
            case "esc", "enter":
                m.state = stateList
                m.filterInput.Blur()
                return m, nil
            default:
                var cmd tea.Cmd
                m.filterInput, cmd = m.filterInput.Update(msg)
                return m, cmd
            }

        case stateConfirmDelete:
            switch msg.String() {
            case "y", "Y":
                if item, ok := m.list.SelectedItem().(sessionItem); ok {
                    m.selected = &item.info
                }
                return m, tea.Quit
            default:
                m.state = stateList
                return m, nil
            }

        case stateList:
            switch msg.String() {
            case "q", "esc":
                return m, tea.Quit

            case "enter":
                if item, ok := m.list.SelectedItem().(sessionItem); ok {
                    m.selected = &item.info
                }
                return m, tea.Quit

            case "x":
                if _, ok := m.list.SelectedItem().(sessionItem); ok {
                    m.state = stateConfirmDelete
                }
                return m, nil

            case "/":
                m.filterInput.SetValue("")
                m.filterInput.Focus()
                m.state = stateFilter
                return m, textinput.Blink

            case "j", "down":
                m.list.CursorDown()
                if _, ok := m.list.SelectedItem().(groupHeader); ok {
                    m.list.CursorDown()
                }
                return m, nil

            case "k", "up":
                m.list.CursorUp()
                if _, ok := m.list.SelectedItem().(groupHeader); ok {
                    m.list.CursorUp()
                }
                return m, nil
            }
        }
    }

    var cmd tea.Cmd
    m.list, cmd = m.list.Update(msg)
    return m, cmd
}

func (m overlayModel) View() string {
    var b strings.Builder

    if m.state == stateFilter {
        b.WriteString("Filter: ")
        b.WriteString(m.filterInput.View())
        b.WriteString("\n\n")
    }

    b.WriteString(m.list.View())

    if m.state == stateConfirmDelete {
        if item, ok := m.list.SelectedItem().(sessionItem); ok {
            b.WriteString("\n")
            b.WriteString(lipgloss.NewStyle().
                Foreground(lipgloss.Color("#ff5f5f")).
                Render(fmt.Sprintf("Delete '%s'? [y/N]", item.info.Name)))
        }
    }

    helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
    b.WriteString("\n")
    b.WriteString(helpStyle.Render("enter attach  x delete  / filter  q quit"))

    return b.String()
}

func (c *Client) RunOverlay(sessions []protocol.SessionInfo) *OverlayResult {
    m := newOverlayModel(sessions)
    p := tea.NewProgram(m, tea.WithAltScreen())

    final, err := p.Run()
    if err != nil {
        return nil
    }

    result := final.(overlayModel)
    if result.selected != nil {
        action := "attach"
        if result.state == stateConfirmDelete {
            action = "delete"
        }
        return &OverlayResult{
            Action:    action,
            SessionID: result.selected.ID,
        }
    }

    return nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: compiles

- [ ] **Step 3: Add shell-in-worktree support**

Create `internal/client/shell.go`:

```go
// internal/client/shell.go
package client

import (
    "os"
    "os/exec"
)

func runShellInWorktree(worktreePath string) error {
    shell := os.Getenv("SHELL")
    if shell == "" {
        shell = "/bin/sh"
    }

    cmd := exec.Command(shell)
    cmd.Dir = worktreePath
    cmd.Stdin = os.Stdin
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    cmd.Env = append(os.Environ(), "GRAITH_WORKTREE="+worktreePath)

    return cmd.Run()
}
```

This spawns the user's shell in the worktree directory. The `GRAITH_WORKTREE` env var lets shell prompts show context. Terminal is already in cooked mode when this runs (passthrough restores it before returning).

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: add bubbletea overlay UI and shell-in-worktree shortcut"
```

---

### Task 13: CLI commands

**Files:**
- Modify: `internal/cli/root.go`
- Create: `internal/cli/new.go`
- Create: `internal/cli/list.go`
- Create: `internal/cli/attach.go`
- Create: `internal/cli/delete.go`
- Create: `internal/cli/rename.go`
- Create: `internal/cli/info.go`
- Create: `internal/cli/doctor.go`
- Create: `internal/cli/daemon.go`

- [ ] **Step 1: Update root command with shared state**

```go
// internal/cli/root.go
package cli

import (
    "os"

    "github.com/dougalmatthews/graith/internal/config"
    "github.com/dougalmatthews/graith/internal/output"
    "github.com/dougalmatthews/graith/internal/version"
    "github.com/spf13/cobra"
)

var (
    cfgFile    string
    jsonOutput bool
    cfg        *config.Config
    paths      config.Paths
    out        *output.Writer
)

var rootCmd = &cobra.Command{
    Use:     "gr",
    Short:   "graith — AI agent session manager",
    Version: version.Version,
    PersistentPreRun: func(cmd *cobra.Command, args []string) {
        cfg = config.LoadOrDefault(cfgFile)
        paths = config.ResolvePaths()
        out = output.New(jsonOutput)
    },
    RunE: func(cmd *cobra.Command, args []string) error {
        return runAttach(cmd, "")
    },
}

func Execute() error {
    return rootCmd.Execute()
}

func init() {
    rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
    rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
}
```

- [ ] **Step 2: Implement `gr new`**

```go
// internal/cli/new.go
package cli

import (
    "fmt"
    "os"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var (
    newAgent      string
    newBase       string
    newBackground bool
)

var newCmd = &cobra.Command{
    Use:   "new <name>",
    Short: "Create a new agent session",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        name := args[0]

        cwd, _ := os.Getwd()
        agent := newAgent
        if agent == "" {
            agent = cfg.DefaultAgent
        }

        c, err := client.New(cfg, paths)
        if err != nil {
            return err
        }
        defer c.Close()

        if err := c.Handshake(); err != nil {
            return err
        }
        c.ReadControlResponse()

        c.SendControl("create", protocol.CreateMsg{
            Name:     name,
            Agent:    agent,
            RepoPath: cwd,
            Base:     newBase,
        })

        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }

        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        var info protocol.SessionInfo
        protocol.DecodePayload(resp, &info)

        if jsonOutput {
            out.JSON(info)
            return nil
        }

        out.Print("Created session %s (%s) in %s\n", info.Name, info.ID, info.WorktreePath)

        if newBackground {
            return nil
        }

        return runAttachByID(c, info.ID)
    },
}

func init() {
    rootCmd.AddCommand(newCmd)
    newCmd.Flags().StringVar(&newAgent, "agent", "", "agent to use")
    newCmd.Flags().StringVar(&newBase, "base", "", "base branch")
    newCmd.Flags().BoolVar(&newBackground, "background", false, "create without attaching")
}
```

- [ ] **Step 3: Implement `gr list`**

```go
// internal/cli/list.go
package cli

import (
    "fmt"
    "sort"
    "text/tabwriter"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var listRepo string

var listCmd = &cobra.Command{
    Use:     "list",
    Aliases: []string{"ls"},
    Short:   "List all sessions",
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.New(cfg, paths)
        if err != nil {
            return err
        }
        defer c.Close()

        c.Handshake()
        c.ReadControlResponse()

        c.SendControl("list", struct{}{})
        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }

        var list protocol.SessionListMsg
        protocol.DecodePayload(resp, &list)

        if jsonOutput {
            return out.JSON(list)
        }

        if len(list.Sessions) == 0 {
            out.Print("No sessions. Create one with: gr new <name>\n")
            return nil
        }

        sort.Slice(list.Sessions, func(i, j int) bool {
            if list.Sessions[i].RepoName != list.Sessions[j].RepoName {
                return list.Sessions[i].RepoName < list.Sessions[j].RepoName
            }
            return list.Sessions[i].Name < list.Sessions[j].Name
        })

        tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
        fmt.Fprintln(tw, "REPO\tNAME\tAGENT\tSTATUS\tID")
        for _, s := range list.Sessions {
            fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.RepoName, s.Name, s.Agent, s.Status, s.ID)
        }
        tw.Flush()

        return nil
    },
}

func init() {
    rootCmd.AddCommand(listCmd)
    listCmd.Flags().StringVar(&listRepo, "repo", "", "filter by repo path")
}
```

- [ ] **Step 4: Implement `gr attach`**

```go
// internal/cli/attach.go
package cli

import (
    "context"
    "fmt"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
    Use:   "attach [name]",
    Short: "Attach to a session",
    Args:  cobra.MaximumNArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        name := ""
        if len(args) > 0 {
            name = args[0]
        }
        return runAttach(cmd, name)
    },
}

func init() {
    rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, name string) error {
    c, err := client.New(cfg, paths)
    if err != nil {
        return err
    }
    defer c.Close()

    if err := c.Handshake(); err != nil {
        return err
    }
    c.ReadControlResponse()

    c.SendControl("list", struct{}{})
    resp, err := c.ReadControlResponse()
    if err != nil {
        return err
    }

    var list protocol.SessionListMsg
    protocol.DecodePayload(resp, &list)

    if len(list.Sessions) == 0 {
        out.Print("No sessions. Create one with: gr new <name>\n")
        return nil
    }

    if name == "" {
        result := c.RunOverlay(list.Sessions)
        if result == nil {
            return nil
        }
        if result.Action == "delete" {
            c.SendControl("delete", protocol.DeleteMsg{SessionID: result.SessionID})
            c.ReadControlResponse()
            return nil
        }
        return runAttachByID(c, result.SessionID)
    }

    for _, s := range list.Sessions {
        if s.Name == name || s.ID == name {
            return runAttachByID(c, s.ID)
        }
    }

    return fmt.Errorf("session %q not found", name)
}

func runAttachByID(c *client.Client, sessionID string) error {
    c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})
    resp, err := c.ReadControlResponse()
    if err != nil {
        return err
    }
    if resp.Type == "error" {
        var e protocol.ErrorMsg
        protocol.DecodePayload(resp, &e)
        return fmt.Errorf("%s", e.Message)
    }

    ctx := context.Background()
    prefixByte := byte(0x02) // ctrl+b

    for {
        result := c.RunPassthrough(ctx, prefixByte)
        switch result {
        case client.ResultOverlay:
            c.SendControl("list", struct{}{})
            listResp, err := c.ReadControlResponse()
            if err != nil {
                return err
            }
            var list protocol.SessionListMsg
            protocol.DecodePayload(listResp, &list)

            overlayResult := c.RunOverlay(list.Sessions)
            if overlayResult == nil {
                continue
            }
            if overlayResult.Action == "delete" {
                c.SendControl("delete", protocol.DeleteMsg{SessionID: overlayResult.SessionID})
                c.ReadControlResponse()
                continue
            }
            c.SendControl("attach", protocol.AttachMsg{SessionID: overlayResult.SessionID})
            c.ReadControlResponse()
            continue

        case client.ResultShell:
            c.SendControl("list", struct{}{})
            infoResp, _ := c.ReadControlResponse()
            var infoList protocol.SessionListMsg
            protocol.DecodePayload(infoResp, &infoList)
            // Find the worktree path for the currently attached session
            for _, s := range infoList.Sessions {
                if s.ID == sessionID {
                    runShellInWorktree(s.WorktreePath)
                    break
                }
            }
            continue

        case client.ResultDetached, client.ResultQuit:
            return nil
        }
    }
}
```

- [ ] **Step 5: Implement `gr delete`**

```go
// internal/cli/delete.go
package cli

import (
    "fmt"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
    Use:   "delete <name>",
    Short: "Delete a session",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.New(cfg, paths)
        if err != nil {
            return err
        }
        defer c.Close()

        c.Handshake()
        c.ReadControlResponse()

        c.SendControl("list", struct{}{})
        listResp, _ := c.ReadControlResponse()
        var list protocol.SessionListMsg
        protocol.DecodePayload(listResp, &list)

        var sessionID string
        for _, s := range list.Sessions {
            if s.Name == args[0] || s.ID == args[0] {
                sessionID = s.ID
                break
            }
        }
        if sessionID == "" {
            return fmt.Errorf("session %q not found", args[0])
        }

        c.SendControl("delete", protocol.DeleteMsg{SessionID: sessionID})
        resp, err := c.ReadControlResponse()
        if err != nil {
            return err
        }
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        out.Print("Session deleted\n")
        return nil
    },
}

func init() {
    rootCmd.AddCommand(deleteCmd)
}
```

- [ ] **Step 6: Implement `gr rename`**

```go
// internal/cli/rename.go
package cli

import (
    "fmt"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
    Use:   "rename <old> <new>",
    Short: "Rename a session",
    Args:  cobra.ExactArgs(2),
    RunE: func(cmd *cobra.Command, args []string) error {
        c, err := client.New(cfg, paths)
        if err != nil {
            return err
        }
        defer c.Close()

        c.Handshake()
        c.ReadControlResponse()

        c.SendControl("list", struct{}{})
        listResp, _ := c.ReadControlResponse()
        var list protocol.SessionListMsg
        protocol.DecodePayload(listResp, &list)

        var sessionID string
        for _, s := range list.Sessions {
            if s.Name == args[0] || s.ID == args[0] {
                sessionID = s.ID
                break
            }
        }
        if sessionID == "" {
            return fmt.Errorf("session %q not found", args[0])
        }

        c.SendControl("rename", protocol.RenameMsg{SessionID: sessionID, NewName: args[1]})
        resp, _ := c.ReadControlResponse()
        if resp.Type == "error" {
            var e protocol.ErrorMsg
            protocol.DecodePayload(resp, &e)
            return fmt.Errorf("%s", e.Message)
        }

        out.Print("Renamed to %s\n", args[1])
        return nil
    },
}

func init() {
    rootCmd.AddCommand(renameCmd)
}
```

- [ ] **Step 7: Implement `gr info`**

```go
// internal/cli/info.go
package cli

import (
    "fmt"
    "os"
    "strings"

    "github.com/dougalmatthews/graith/internal/client"
    "github.com/dougalmatthews/graith/internal/protocol"
    "github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
    Use:   "info",
    Short: "Show current session info",
    RunE: func(cmd *cobra.Command, args []string) error {
        cwd, _ := os.Getwd()

        c, err := client.New(cfg, paths)
        if err != nil {
            return err
        }
        defer c.Close()

        c.Handshake()
        c.ReadControlResponse()

        c.SendControl("list", struct{}{})
        resp, _ := c.ReadControlResponse()
        var list protocol.SessionListMsg
        protocol.DecodePayload(resp, &list)

        for _, s := range list.Sessions {
            if strings.HasPrefix(cwd, s.WorktreePath) {
                if jsonOutput {
                    return out.JSON(s)
                }
                out.Print("Session:   %s (%s)\n", s.Name, s.ID)
                out.Print("Agent:     %s\n", s.Agent)
                out.Print("Repo:      %s\n", s.RepoName)
                out.Print("Branch:    %s\n", s.Branch)
                out.Print("Worktree:  %s\n", s.WorktreePath)
                out.Print("Status:    %s\n", s.Status)
                return nil
            }
        }

        return fmt.Errorf("not inside a graith session worktree")
    },
}

func init() {
    rootCmd.AddCommand(infoCmd)
}
```

- [ ] **Step 8: Implement `gr daemon`**

```go
// internal/cli/daemon.go
package cli

import (
    "fmt"

    "github.com/dougalmatthews/graith/internal/daemon"
    "github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
    Use:   "daemon",
    Short: "Manage the graith daemon",
}

var daemonStartCmd = &cobra.Command{
    Use:   "start",
    Short: "Start the daemon",
    RunE: func(cmd *cobra.Command, args []string) error {
        return daemon.Run(cfg, paths)
    },
}

var daemonStopCmd = &cobra.Command{
    Use:   "stop",
    Short: "Stop the daemon",
    RunE: func(cmd *cobra.Command, args []string) error {
        out.Print("Stopping daemon...\n")
        // Send SIGTERM to PID in pidfile
        return fmt.Errorf("not yet implemented")
    },
}

func init() {
    rootCmd.AddCommand(daemonCmd)
    daemonCmd.AddCommand(daemonStartCmd)
    daemonCmd.AddCommand(daemonStopCmd)
}
```

- [ ] **Step 9: Implement `gr doctor`**

```go
// internal/cli/doctor.go
package cli

import (
    "fmt"
    "net"
    "os"
    "time"

    "github.com/spf13/cobra"
)

var doctorAutofix bool

var doctorCmd = &cobra.Command{
    Use:   "doctor",
    Short: "Health checks and diagnostics",
    RunE: func(cmd *cobra.Command, args []string) error {
        ok := true

        out.Print("Checking graith health...\n\n")

        if _, err := os.Stat(paths.SocketPath); err == nil {
            conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
            if err != nil {
                out.Print("  ✗ Socket exists but daemon not responding: %s\n", paths.SocketPath)
                if doctorAutofix {
                    os.Remove(paths.SocketPath)
                    out.Print("    → Removed stale socket\n")
                }
                ok = false
            } else {
                conn.Close()
                out.Print("  ✓ Daemon is running\n")
            }
        } else {
            out.Print("  ○ Daemon not running (will auto-start on first command)\n")
        }

        if _, err := os.Stat(paths.ConfigFile); err == nil {
            out.Print("  ✓ Config file: %s\n", paths.ConfigFile)
        } else {
            out.Print("  ○ No config file (using defaults): %s\n", paths.ConfigFile)
        }

        out.Print("  ✓ Data dir: %s\n", paths.DataDir)
        out.Print("  ✓ Runtime dir: %s\n", paths.RuntimeDir)

        if !ok {
            return fmt.Errorf("issues found")
        }

        out.Print("\nAll checks passed.\n")
        return nil
    },
}

func init() {
    rootCmd.AddCommand(doctorCmd)
    doctorCmd.Flags().BoolVar(&doctorAutofix, "autofix", false, "auto-fix issues")
}
```

- [ ] **Step 10: Verify everything compiles**

Run: `go build -o gr ./cmd/graith && ./gr --help`
Expected: shows help with all subcommands

- [ ] **Step 11: Commit**

```bash
git add -A && git commit -m "feat: add all CLI commands (new, list, attach, delete, rename, info, doctor, daemon)"
```

---

## Milestone 5: Integration & Polish

### Task 14: End-to-end smoke test

- [ ] **Step 1: Build the binary**

Run: `go build -o gr ./cmd/graith`

- [ ] **Step 2: Test `gr doctor`**

Run: `./gr doctor`
Expected: shows health check output

- [ ] **Step 3: Test `gr list` (starts daemon)**

Run: `./gr list`
Expected: "No sessions" message, daemon auto-started

- [ ] **Step 4: Test `gr new` in a git repo**

Run: `cd /tmp && git init test-repo && cd test-repo && git commit --allow-empty -m "init" && /Users/dougalmatthews/Code/graith/gr new test-session --agent claude --background`
Expected: creates session, shows session info

- [ ] **Step 5: Test `gr list` shows the session**

Run: `./gr list`
Expected: shows the test session

- [ ] **Step 6: Test `gr rename`**

Run: `./gr rename test-session renamed-session`
Expected: success

- [ ] **Step 7: Test `gr delete`**

Run: `./gr delete renamed-session`
Expected: session deleted

- [ ] **Step 8: Run all tests**

Run: `go test ./... -v -timeout 60s`
Expected: all pass

- [ ] **Step 9: Commit any fixes**

```bash
git add -A && git commit -m "fix: integration test fixes"
```

---

## v1 Acceptance Criteria

The following must work for v1 to be considered complete:

- [ ] `gr new <name>` creates a session with git worktree isolation
- [ ] `gr list` shows all sessions grouped by repo
- [ ] `gr attach` opens the overlay and allows session switching
- [ ] `gr delete` with confirmation removes session, worktree, and branch
- [ ] `gr rename` changes display name
- [ ] `gr info --json` returns session info from within a worktree
- [ ] `gr doctor` reports health status
- [ ] Daemon auto-starts and persists sessions across terminal closures
- [ ] Passthrough mode forwards all input/output with no interference
- [ ] Prefix key (ctrl+b) triggers overlay without breaking agent TUI
- [ ] Multiple clients can view different sessions simultaneously
- [ ] Agent process exit is detected and session marked as stopped
