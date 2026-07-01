---
weight: 200
title: "Installation"
description: "Install graith and the gr CLI."
icon: "download"
toc: true
draft: false
---

The binary is called `gr`.

## Install

{{< tabs tabTotal="4" >}}
{{% tab tabName="Homebrew" %}}

```bash
brew install d0ugal/tap/graith
```

{{% /tab %}}
{{% tab tabName="Prebuilt binary" %}}

Download a binary for your platform from the [releases page](https://github.com/d0ugal/graith/releases), extract it, and place `gr` on your `$PATH`.

{{% /tab %}}
{{% tab tabName="go install" %}}

```bash
go install github.com/d0ugal/graith/cmd/graith@latest
```

`go install` names the binary after the package directory (`graith`). Rename or symlink it to `gr`:

```bash
mv "$(go env GOPATH)/bin/graith" "$(go env GOPATH)/bin/gr"
```

{{% /tab %}}
{{% tab tabName="From source" %}}

```bash
git clone https://github.com/d0ugal/graith
cd graith
make build    # produces ./gr
```

{{% /tab %}}
{{< /tabs >}}

## Shell completion

Generate completion scripts for your shell:

```bash
# bash
source <(gr completion bash)

# zsh
gr completion zsh > "${fpath[1]}/_gr"

# fish
gr completion fish | source

# powershell
gr completion powershell | Out-String | Invoke-Expression
```

## Verify

```bash
gr version
gr doctor      # health checks, verifies dependencies
```

{{% alert context="info" text="`gr doctor --autofix` will attempt to fix common issues: truncate oversized logs, clean stale PID files, and remove orphaned worktrees." /%}}
