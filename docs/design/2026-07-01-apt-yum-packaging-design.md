---
title: "Design Doc: apt & yum/dnf Packaging"
authors: Dougal Matthews
created: 2026-07-01
status: Draft
reviewers: (none yet)
informed: (TBD)
---

# apt & yum/dnf Packaging

## Background

graith ships as a single Go binary (`gr`, module `github.com/d0ugal/graith`).
Releases are cut by pushing a `v*` tag, which triggers
`.github/workflows/goreleaser.yml`. GoReleaser (`~> v2`, config in
`.goreleaser.yaml`) builds `linux`/`darwin` × `amd64`/`arm64`, publishes
`.tar.gz` archives plus a `checksums.txt` to the GitHub Release, and pushes a
Homebrew formula to `d0ugal/homebrew-tap` (using `RELEASE_TOKEN`, a PAT with
`contents:write` on the tap, because the default `GITHUB_TOKEN` can't push
cross-repo).

Today Linux users install graith one of three ways: download a tarball from the
Release page, `go install`, or (on macOS/Linuxbrew) `brew install
d0ugal/tap/graith`. There is no native OS package. Users on Debian/Ubuntu and
Fedora/RHEL have no way to `apt-get install graith` / `dnf install graith`, get
no automatic updates through the system package manager, and have to manage the
binary and shell completions by hand.

## Problem

- **No native install path on Linux.** The two common enterprise/desktop
  distribution families (Debian/Ubuntu, Fedora/RHEL) have no first-class
  install. Tarball installs don't self-update and don't drop completions in the
  right place; `go install` requires a Go toolchain and names the binary
  `graith` not `gr`.
- **Completions aren't packaged.** `gr completion {bash,zsh,fish}` exists
  (`internal/cli/completion.go`) but nothing installs the generated scripts into
  the standard system directories, so tab-completion doesn't "just work" after
  install.
- **Updates are manual.** Without a repo, there's no `apt-get upgrade` /
  `dnf upgrade` story — users have to notice a release and re-download.

The goal is to add `.deb` and `.rpm` packages and a way to install/upgrade them,
in a way that fits the existing GitHub-centric, GoReleaser-driven, low-touch
release machinery — not a bespoke build farm.

## Goals

- Produce `.deb` and `.rpm` packages for `amd64` and `arm64` from the existing
  GoReleaser release, attached to the GitHub Release like the tarballs are.
- Package the `gr` binary, the bash/zsh/fish completions, the `LICENSE`, and a
  minimal doc set into the correct FHS locations.
- Offer users a repo they can add once and then `apt-get install graith` /
  `dnf install graith`, with `apt-get upgrade` / `dnf upgrade` picking up new
  releases.
- Sign packages and repository metadata with a GPG key, with the key material
  handled as a CI secret and never committed.
- Keep ongoing maintenance and cost close to zero, consistent with how the tap
  works today (no servers to run, no paid tier required).

### Non-Goals

- **No systemd unit.** `gr` is an interactive CLI; the daemon (`graithd`) is
  auto-started on first command and lives in the user's session, not as a
  system service. We do not ship a unit file.
- **No man page (v1).** The CLI has no `GenManTree` today. Generating and
  packaging a man page is deferred (see Open Questions).
- **No Alpine/`apk`, Arch/AUR, Nix, Snap, Flatpak, or Windows packaging** — out
  of scope here; can follow the same pattern later if there's demand.
- **No changes to the binary, the daemon, or the CLI command surface.** This is
  purely packaging and distribution.
- **No mirroring/CDN or Debian/Fedora upstream inclusion.** We are not seeking
  inclusion in official distro archives.

## Proposals

### 1. Build `.deb`/`.rpm` via GoReleaser nfpm

GoReleaser has a built-in [nfpm](https://nfpm.goreleaser.com/) integration via
the `nfpms:` block. It reuses the existing `builds:` output (the `gr` binary),
so no separate build step is needed. Add a block roughly like:

```yaml
nfpms:
  - id: graith
    package_name: graith
    vendor: Dougal Matthews
    homepage: https://github.com/d0ugal/graith
    maintainer: Dougal Matthews <...>
    description: Terminal session manager for AI coding agents
    license: MIT
    formats:
      - deb
      - rpm
    # amd64 + arm64 come from the existing builds matrix; nfpm emits one
    # package per (format, goarch).
    bindir: /usr/bin
    contents:
      # shell completions, generated at release time (see below)
      - src: ./completions/gr.bash
        dst: /usr/share/bash-completion/completions/gr
      - src: ./completions/gr.zsh
        dst: /usr/share/zsh/vendor-completions/_gr
      - src: ./completions/gr.fish
        dst: /usr/share/fish/vendor_completions.d/gr.fish
      - src: ./LICENSE
        dst: /usr/share/doc/graith/copyright
      - src: ./README.md
        dst: /usr/share/doc/graith/README.md
```

Package contents, summary:

| Path | Content |
|------|---------|
| `/usr/bin/gr` | the binary |
| `/usr/share/bash-completion/completions/gr` | bash completion |
| `/usr/share/zsh/vendor-completions/_gr` | zsh completion |
| `/usr/share/fish/vendor_completions.d/gr.fish` | fish completion |
| `/usr/share/doc/graith/copyright` | LICENSE |
| `/usr/share/doc/graith/README.md` | README |

**Generating completions at release time.** The completion scripts are produced
by the built binary itself (`gr completion bash|zsh|fish`). GoReleaser can run a
hook before packaging. Because we cross-compile (e.g. building an arm64 binary
on an amd64 runner), we can't run the cross-built binary — but completion output
is host-arch-independent, so we generate once from a natively built `gr` (or
`go run ./cmd/graith completion ...`) in a `before:` hook and reuse the same
files for every arch:

```yaml
before:
  hooks:
    - mkdir -p completions
    - sh -c 'go run ./cmd/graith completion bash > completions/gr.bash'
    - sh -c 'go run ./cmd/graith completion zsh  > completions/gr.zsh'
    - sh -c 'go run ./cmd/graith completion fish > completions/gr.fish'
```

**Package naming.** We name the package `graith` (matching the tap) and ship
the binary as `gr`. The `gr` name itself is unclaimed on Debian — there is no
package named `gr` in the archive and no Debian package installs a `/usr/bin/gr`
(or `/bin/gr`) binary (only unrelated `gr-*` GNU Radio packages such as `gr-gsm`
and `gr-osmosdr` exist, none of which is `gr` or ships a `gr` binary). So a
package named `gr` would not collide today. We still choose `graith` for
branding and discoverability (it matches the project name, the module, and the
Homebrew tap) and to avoid a cryptic two-letter package name that's hard to
search for and easy to confuse with the unrelated `gr-*` family; shipping the
binary as `gr` keeps the command users actually type unchanged.

**Version metadata.** nfpm derives the package version from the tag the same way
the archives do; the existing `ldflags` `-X ...version.Version={{.Version}}`
already stamps the binary, so `gr --version` and the package version agree.

### 2. Repo hosting: self-hosted static repo on GitHub Pages (recommended)

The realistic options and their trade-offs:

| Option | Cost | Maintenance | Fits current setup | Notes |
|--------|------|-------------|--------------------|-------|
| **Self-hosted static repo on GitHub Pages** | Free | Low (fully in CI) | High — GitHub-native, mirrors the tap model | We generate signed apt/yum repo metadata in CI and publish to a `gh-pages` branch / a `d0ugal/apt` (+ `d0ugal/yum`) repo. |
| Gemfury / Fury.io | Free tier, then paid | Very low (they host + sign) | Medium | GoReleaser has first-class Gemfury push support (the `gemfury:` block, a GoReleaser **Pro** feature). Free tier has limits; vendor lock-in for the URL. |
| Cloudsmith | Free OSS tier, else paid | Very low | Medium | Polished, supports many formats. The open-source plan is self-serve (create the repo; eligibility is verified after the fact — no advance application/approval). |
| packagecloud.io | Free tier + paid; OSS program | Very low | Medium | Mature; historically the "just works" option. Has a perpetual free tier (10 GB bandwidth / 2 GB storage) plus a request-based "free for open source" program; paid plans above that. |
| Artifactory / self-run server | $$ / server ops | High | Low | Overkill; contradicts the "no servers" ethos. |

**Recommendation: self-hosted static repo published to GitHub Pages.** It is
free, requires no third-party account, keeps everything inside the GitHub
`d0ugal/*` org (consistent with `homebrew-tap`), and the whole flow lives in the
release CI so day-to-day maintenance is zero. The trade-off is that we own the
repo-metadata generation and signing (more moving parts in CI than a hosted
service), and GitHub Pages has published limits — a 100 GB/month bandwidth soft
limit and a 1 GB published-site size cap (plus the backing repo's 100 MiB
per-file limit) — all comfortably fine at graith's scale, revisit only if
downloads grow large.

**Layout.** A dedicated repo `d0ugal/graith-repo` (or reuse the
`homebrew-tap` org pattern with `d0ugal/apt` and `d0ugal/yum`) served via
GitHub Pages at, say, `https://d0ugal.github.io/graith-repo/`:

```
/deb/                       apt repo root
  dists/stable/...          Release, Release.gpg, InRelease, Packages(.gz)
  pool/main/g/graith/*.deb
/rpm/                       yum/dnf repo root
  repodata/repomd.xml{,.asc}
  graith-<ver>.<arch>.rpm
/gpg/graith.gpg             public signing key (ASCII-armored)
```

**Generating repo metadata in CI.** After GoReleaser produces the packages, a
follow-on job builds the repo indexes:

- **apt:** use `aptly` (or `reprepro`) to add the `.deb`s to a repo, generate
  `Packages`/`Release`, and sign `Release` → `Release.gpg` / `InRelease`.
- **rpm:** use `createrepo_c` to build `repodata/`, then `gpg --detach-sign
  --armor repomd.xml` and sign each `.rpm` with `rpm --addsign` (or
  `rpmsign`).

These tools run in the release workflow (in a container / `apt-get install`
step), commit the regenerated tree to the Pages branch, and push. Because the
repo is static files, GitHub Pages just serves it.

### 3. GPG signing & key management

A single project signing key (RSA 4096 or ed25519), created once:

- **Private key** lives only as an encrypted CI secret. Store the
  ASCII-armored private key in a GitHub Actions secret (e.g.
  `GPG_PRIVATE_KEY`) plus its passphrase (`GPG_PASSPHRASE`). The release job
  imports it into a throwaway keyring (`gpg --batch --import`) at the start and
  the runner is ephemeral, so nothing persists.
- **Public key** is committed to the repo tree at `/gpg/graith.gpg` and served
  over the Pages site, so users can fetch and trust it.
- **What gets signed:**
  - apt: the `Release` file (`Release.gpg` + inline `InRelease`).
  - rpm: `repomd.xml` (detached `.asc`) and each `.rpm` package
    (`rpm --addsign`), so `dnf` can verify package signatures, not just repo
    metadata.
- **Key custody / rotation.** The master key is generated locally by the
  maintainer, the private half stored in a password manager as the source of
  truth, and only the CI copy lives in Actions secrets. Rotation = generate a
  new key, publish the new public key, re-sign the repo, and document the
  change; a short overlap where both keys are trusted eases migration.

This mirrors the existing `RELEASE_TOKEN` secret pattern — sensitive material as
an encrypted repo/org secret, never in the tree.

### 4. End-user install UX

**Debian / Ubuntu:**

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

The served key at `/gpg/graith.gpg` should be a binary (dearmored) GPG keyring
so `signed-by=` can read it directly; if we publish the key ASCII-armored (as
the GPG-signing section assumes), either serve a `.asc` and dearmor on the
client (`gpg --dearmor`) or publish a dearmored copy. By convention the local
keyring file is often named `graith-archive-keyring.gpg`; the exact filename is
cosmetic as long as the `signed-by=` path matches.

**Fedora / RHEL:**

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

`dnf upgrade` picks up new releases. We add both blocks to the README `Install`
section next to the existing Homebrew / `go install` instructions.

### 5. CI / release integration

The change slots into the existing single tag-triggered release:

1. Add the `nfpms:` block and completion `before:` hook to `.goreleaser.yaml`.
   `.deb`/`.rpm` are now built and attached to the GitHub Release automatically
   — this alone is useful even before the repo exists (users can download and
   `dpkg -i` / `rpm -i`).
2. Add a follow-on job in `goreleaser.yml` (or a second workflow that keys off
   the release) that:
   - imports the GPG key from secrets,
   - downloads the freshly built `.deb`/`.rpm` (from the Release or the
     GoReleaser `dist/` artifacts),
   - regenerates the apt/yum metadata (`aptly`/`reprepro`, `createrepo_c`),
     signs it,
   - commits and pushes the updated static tree to the Pages repo using
     `RELEASE_TOKEN` (same cross-repo-push reason as the tap).

Signing secrets needed: `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`. Existing
`RELEASE_TOKEN` reused for the cross-repo push. `permissions: contents: write`
already set.

### 6. Maintenance & cost

- **Cost:** $0. GitHub Pages hosting + GitHub Actions minutes only.
- **Maintenance:** effectively none per release — the repo regenerates itself in
  CI. The only recurring human tasks are (a) GPG key rotation (rare) and (b)
  occasional pruning of very old package versions from the repo tree if the
  Pages repo grows large.
- **Repo size growth:** each release adds ~4 packages (2 formats × 2 arches).
  With `aptly`/`createrepo_c` we can retain the last N versions and drop older
  ones to bound the git history / Pages size.

## Other Notes

### Migration / compatibility

Purely additive. Existing install paths (tarball, `go install`, Homebrew) are
untouched. No changes to the binary, so `.deb`/`.rpm` installs behave
identically to a tarball install plus completions. First-time users of the repo
follow the one-time "add key + repo" step above.

### Open questions / decisions to be made

- **Repo hosting host:** confirm GitHub Pages self-hosted vs. adopting Fury.io
  free tier (less CI to own, at the cost of a third-party URL and free-tier
  limits). Recommendation is self-hosted, but Fury is a low-effort fallback if
  the CI metadata generation proves fiddly.
- **Repo layout / naming:** one repo (`d0ugal/graith-repo` with `/deb` +
  `/rpm`) vs. two (`d0ugal/apt`, `d0ugal/yum`) to mirror `homebrew-tap`.
- **apt tooling:** `aptly` (nicer publish/snapshot model, retention) vs.
  `reprepro` (simpler, ships in Debian). Both are viable in CI.
- **Man page:** add `GenManTree` to the CLI and package a man page under
  `/usr/share/man/man1/gr.1`? Deferred to a follow-up unless wanted now.
- **Package name:** confirm `graith` (recommended, matches the tap) vs. `gr`.
  `gr` is unclaimed on Debian (no `gr` package, no package shipping `/usr/bin/gr`),
  so it wouldn't collide, but `graith` is preferred for branding/discoverability
  and to avoid a cryptic two-letter name; either way the binary stays `gr`.
- **Distro codename channels:** ship a single `stable` suite for all Debian/
  Ubuntu releases (simplest; the binary is statically linked Go so it doesn't
  matter) vs. per-codename suites. Recommend single `stable`.

### Recommended phased rollout

1. **Phase 1 — packages only.** Add `nfpms:` + completion hook to
   `.goreleaser.yaml`. `.deb`/`.rpm` attach to each GitHub Release. Document
   manual `dpkg -i` / `rpm -i` install. Low risk, immediately useful, no
   secrets or new repos.
2. **Phase 2 — signed repos.** Stand up the Pages repo, add the GPG secrets and
   the metadata-generation job, publish signed apt/yum repos, and add the
   `apt-get install` / `dnf install` instructions to the README.
3. **Phase 3 — polish.** Retention/pruning of old versions, optional man page,
   and (if demand appears) additional package formats (apk, AUR, etc.) following
   the same GoReleaser-driven pattern.

### References

- `.goreleaser.yaml` — existing `builds:`, `archives:`, `brews:` blocks
- `.github/workflows/goreleaser.yml` — tag-triggered release, `RELEASE_TOKEN`
- `internal/cli/completion.go` — `gr completion {bash,zsh,fish}`
- `README.md` — `Install` section (Homebrew / `go install`)

External / authoritative:

- GoReleaser nfpm integration (`nfpms:` block, `ids`, `formats`, `contents`):
  <https://goreleaser.com/customization/nfpm/>
- nfpm configuration reference: <https://nfpm.goreleaser.com/docs/configuration/>
- GoReleaser global `before:` hooks: <https://goreleaser.com/customization/hooks/>
- GoReleaser Gemfury publishing (`gemfury:` block, GoReleaser Pro):
  <https://goreleaser.com/customization/gemfury/>
- aptly (apt repo management/publish): <https://www.aptly.info/doc/overview/>
- reprepro setup (Debian Wiki):
  <https://wiki.debian.org/DebianRepository/SetupWithReprepro>
- createrepo_c (yum/dnf `repodata/`):
  <https://github.com/rpm-software-management/createrepo_c>
- Debian SecureApt (`Release`/`Release.gpg`/`InRelease`):
  <https://wiki.debian.org/SecureApt>
- Debian third-party repo signing (`signed-by=` keyring, apt-key deprecated):
  <https://wiki.debian.org/DebianRepository/UseThirdParty>
- GPG-signing RPMs and yum repos (`rpm --addsign`, `repomd.xml.asc`):
  <https://blog.packagecloud.io/how-to-gpg-sign-and-verify-rpm-packages-and-yum-repositories/>
- dnf `.repo` configuration reference:
  <https://dnf.readthedocs.io/en/latest/conf_ref.html>
- GitHub Pages limits (bandwidth / size soft limits):
  <https://docs.github.com/en/pages/getting-started-with-github-pages/github-pages-limits>
- Gemfury pricing (free tier): <https://fury.co/pricing/>
- Cloudsmith open-source hosting policy:
  <https://docs.cloudsmith.com/resources/open-source-hosting-policy>
- packagecloud pricing / OSS program: <https://packagecloud.io/pricing/>,
  <https://blog.packagecloud.io/packagecloud-loves-oss/>
- JFrog Artifactory (paid/self-hosted): <https://jfrog.com/artifactory/>
