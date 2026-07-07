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

{{< tabs tabTotal="6" >}}
{{% tab tabName="Homebrew" %}}

```bash
brew install d0ugal/tap/graith
```

{{% /tab %}}
{{% tab tabName="apt (Debian/Ubuntu)" %}}

Add the signing key and repository once, then install with `apt-get`:

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

`apt-get upgrade` then picks up new releases automatically.

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

Download a binary for your platform from the [releases page](https://github.com/d0ugal/graith/releases), extract it, and place `gr` on your `$PATH`.

On Debian/Ubuntu, Fedora/RHEL and Alpine you can instead grab a prebuilt `.deb`, `.rpm` or `.apk` package (linux `amd64` and `arm64`; package name `graith`, binary `gr`, with shell completions), also from the [releases page](https://github.com/d0ugal/graith/releases), and install it manually:

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
