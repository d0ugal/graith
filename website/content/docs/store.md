---
weight: 800
title: "Document Store"
description: "The shared document store agents read and write."
icon: "database"
toc: true
draft: false
---

The document store persists artifacts across sessions. It is a flat-file, git-backed key-value store with per-repo scoping and an optional shared namespace.

## Overview

Documents are plain files on disk, organized in a git repository. Every changed write is committed, so the full history is available via `git log` in the store directory. No-op writes (identical content) are skipped. Files are browsable and greppable with standard tools.

Store operations go directly to disk (not through the daemon), so there is no latency or availability dependency on the daemon process.

## Scoping

By default, documents are scoped to the current repo. Sessions working on the same repo share a store namespace. The repo path is canonicalized (symlinks resolved), so different path spellings for the same repo resolve to the same namespace.

Use `--shared` to access a global store that is not scoped to any repo:

```bash
gr store put --shared prompts/review.md "Review this code..."
gr store get --shared prompts/review.md
gr store ls --shared
```

Use `--repo <path>` to explicitly specify the repo (useful when running outside a session):

```bash
gr store list --repo ~/Code/my-project
```

## Operations

### Put

```bash
# Store inline content
gr store put design/api.md "# API Design\n\nEndpoints: ..."

# Store from file
gr store put design/api.md --file ./api-design.md

# Store from stdin
echo '{"score": 85}' | gr store put results/score.json
```

### Get

```bash
gr store get design/api.md
```

Outputs the raw document body to stdout.

### List

```bash
gr store list                 # list all documents in the current repo's store
gr store list design/         # list documents under a prefix
gr store ls                   # alias
gr store list --all           # list across all repos
```

### Append

```bash
gr store append logs/builds.jsonl '{"status":"pass","ts":"2026-06-16"}'
echo '{"run":2}' | gr store append logs/builds.jsonl
gr store append logs/builds.jsonl --file ./result.json
```

Creates the document if it does not exist. Appends the content followed by a newline. Useful for JSONL-style log data where each entry is one JSON line.

### Remove

```bash
gr store rm design/api.md
```

Removes the document and commits the deletion.

## Key format

Keys are slash-separated paths and should include a file extension to indicate content type:

```
design/api.md
logs/builds.jsonl
tribunal/2026-06-15.json
notes/architecture.md
```

Validation rules:
- Must not be empty
- Must not start with `/` or `-`
- Must not contain `..`, `.git`, or `.` as path components
- Must not contain control characters, NUL bytes, or backslashes
- Must not contain git pathspec characters (`*`, `?`, `[`, `:`)
- Must not be `store.lock`

## Storage location

| Scope | Path |
|-------|------|
| Per-repo | `<data_dir>/store/<repo-name>-<hash>/` |
| Shared | `<data_dir>/store/shared/` |

Each store directory is a git repository initialized by graith. The git user is set to `graith <graith@localhost>`.

## Patterns

### Design documents

Share design decisions across agent sessions:

```bash
gr store put design/auth-rewrite.md --file ./auth-design.md
# Any session on the same repo can read it:
gr store get design/auth-rewrite.md
```

### Build/test logs

Accumulate results in JSONL format:

```bash
gr store append logs/test-runs.jsonl '{"session":"fix-auth","status":"pass","duration":"45s"}'
gr store append logs/test-runs.jsonl '{"session":"add-tests","status":"fail","duration":"12s"}'
```

### Cross-repo artifacts

Use `--shared` for artifacts that span repos:

```bash
gr store put --shared prompts/code-review.md "Review this code for..."
gr store put --shared config/tribunal-rubric.json '{"dimensions":["correctness","style"]}'
```

### Research notes

Persist findings that should survive session deletion:

```bash
gr store put research/auth-middleware.md "$(cat <<'EOF'
# Auth Middleware Investigation

The token refresh logic in middleware.go:145 has a race condition.
Two goroutines can both see an expired token and both attempt refresh.

Fix: use sync.Once or a mutex around the refresh call.
EOF
)"
```
