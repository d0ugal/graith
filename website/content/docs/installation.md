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

On macOS 13 or newer the formula also installs the signed, notarized headless
`Graith.app` used for the per-user daemon. Installation does not register or
start a background item; the first eligible `gr` command does that in the
logged-in user session. No root/system service or visible app UI is installed.

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

For a Darwin tarball, keep `Graith.app` beside `gr`. The first command validates
the matching signature/version/payload and copies the untouched app to a
versioned user cache, so deleting the extracted directory later does not break
an already registered daemon generation. A present but invalid or mismatched
app fails closed; it is not treated as a source-build fallback.

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

Source and `go install` builds do not manufacture or register an unsigned app;
on macOS they explicitly use the existing direct-spawn fallback.

{{% /tab %}}
{{< /tabs >}}

## macOS upgrade, rollback, and uninstall

Normal Homebrew and tarball upgrades preserve service registrations. The new
CLI validates and caches its matching signed app before a preserved restart;
registration moves to that generation only while the job is down. The previous
registered generation remains cached for rollback. If an older release cannot
read newer persisted state, the existing state-version guard stops the
downgrade and `gr doctor` lists the state backup to restore.

Before uninstalling, remove all per-user registrations while the signed package
is still available:

```bash
gr daemon service status --all-profiles
gr daemon service remove --all-profiles
brew uninstall graith            # or remove the tarball files
```

Removal preserves all user data. Homebrew has no supported pre-uninstall hook,
so it cannot do the logged-in user's Service Management cleanup automatically.
If the package was removed first, reinstall the same or a newer signed release
and run `gr daemon service repair`, then `remove --all-profiles`. Do not use a
wildcard `launchctl` command: named profiles are independent exact jobs and an
unknown live job is intentionally quarantined.

### Native stable and `graith-dev` releases

The normal stable release and moving `dev` release use the isolated libghostty
backend on macOS arm64 and Linux amd64/arm64. The normal Intel macOS artifact
remains the supported pure-Go Charm build; it is not a rollback artifact.

For stable installations, Homebrew and the package repositories select the
matching `graith_<version>_darwin_arm64.tar.gz` or Linux tar/deb/rpm/apk asset.
For the moving channel, Homebrew selects `graith-dev_darwin_arm64.tar.gz`,
`graith-dev_linux_amd64.tar.gz`, or `graith-dev_linux_arm64.tar.gz` for those
native targets. Each native archive contains its final executable, normal
release metadata, executable-bound `libghostty-native.spdx.json`, and
`THIRD_PARTY_NOTICES.libghostty.md`. Stable Linux deb/rpm/apk packages carry the
same executable bytes and native evidence. The published `checksums.txt` binds
every archive/package, and GitHub build provenance can be verified after
download:

```bash
gh attestation verify graith-dev_linux_amd64.tar.gz --repo d0ugal/graith
grep '  graith-dev_linux_amd64.tar.gz$' checksums.txt > archive-checksum.txt
test "$(wc -l < archive-checksum.txt)" -eq 1
sha256sum --check archive-checksum.txt
```

For a stable download, substitute its exact versioned filename, for example
`graith_0.70.0_linux_amd64.tar.gz`; the same attestation and checksum commands
apply. Verify a downloaded deb/rpm/apk by selecting its exact line from the same
ten-entry stable `checksums.txt`.

Use `graith-dev_linux_arm64.tar.gz` on arm64. The release workflow builds each
Linux artifact from the exact pinned Ghostty/Zig dependency unit on Linux,
compares the executable across tar/deb/rpm/apk, and executes those final bytes
on the target architecture before publication. Stable releases remain drafts
until the complete same-revision set, checksums, provenance, configured signing,
and downstream metadata have been prepared and validated. The complete GitHub
release is exposed before that metadata is pushed, so its URLs never point at
private draft assets; interrupted channel pushes are safe to retry.

After restarting the `dev` daemon, verify the selected canary without relying
on a live helper process:

```bash
gr-dev doctor
gr-dev doctor --json | jq -r .terminal_backend
```

Native stable/dev artifacts report `libghostty-helper`; Intel macOS stable/dev
artifacts report `charm`. See
[Troubleshooting]({{< relref "/docs/troubleshooting.md#verify-the-terminal-backend" >}})
for the matching startup and failure log records.

The dev and stable releases do not publish separately named rollback archives.
Persistent scrollback remains backend-neutral, so a fresh native start,
Charm-to-native upgrade, or native-to-native upgrade needs no state conversion.
On macOS, remove the relevant service registration before uninstalling its
package as described above.

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
