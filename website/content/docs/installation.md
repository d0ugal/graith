---
weight: 200
title: "Installation"
description: "Install graith and the gr CLI."
icon: "download"
toc: true
draft: false
---

The binary's called `gr`.

## Install

{{< tabs tabTotal="6" >}}
{{% tab tabName="Homebrew" %}}

```bash
brew install d0ugal/tap/graith
```

{{% /tab %}}
{{% tab tabName="apt (Debian/Ubuntu)" %}}

One-time setup, then install:

```bash
# add the signing key
curl -fsSL https://d0ugal.github.io/graith-repo/gpg/graith.gpg \
  | sudo tee /usr/share/keyrings/graith.gpg > /dev/null

# add the repo (signed-by pins it to our key only)
echo "deb [signed-by=/usr/share/keyrings/graith.gpg] \
https://d0ugal.github.io/graith-repo/deb stable main" \
  | sudo tee /etc/apt/sources.list.d/graith.list

sudo apt-get update
sudo apt-get install graith
```

`apt-get upgrade` then picks up new releases.

{{% /tab %}}
{{% tab tabName="dnf (Fedora/RHEL)" %}}

```bash
sudo tee /etc/yum.repos.d/graith.repo <<'EOF'
[graith]
name=graith
baseurl=https://d0ugal.github.io/graith-repo/rpm
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://d0ugal.github.io/graith-repo/gpg/graith.asc
EOF

sudo dnf install graith
```

`dnf upgrade` picks up new releases.

{{% /tab %}}
{{% tab tabName="Prebuilt binary" %}}

Download your platform's binary from the [releases page](https://github.com/d0ugal/graith/releases), extract it, and put `gr` on your `$PATH`.

Debian/Ubuntu, Fedora/RHEL and Alpine users can instead grab a prebuilt `.deb`, `.rpm` or `.apk` from the [releases page](https://github.com/d0ugal/graith/releases) (linux `amd64`/`arm64`; package `graith`, binary `gr`, with shell completions):

```bash
# Debian / Ubuntu
sudo dpkg -i graith_*_linux_amd64.deb

# Fedora / RHEL
sudo rpm -i graith_*_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted graith_*_linux_amd64.apk
```

{{% /tab %}}
{{% tab tabName="go install" %}}

```bash
go install github.com/d0ugal/graith/cmd/graith@latest
```

`go install` names the binary `graith`; rename or symlink it to `gr`:

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

{{% alert context="info" text="`gr doctor --autofix` fixes common issues: truncates oversized logs, cleans stale PID files, and removes orphaned worktrees." /%}}
