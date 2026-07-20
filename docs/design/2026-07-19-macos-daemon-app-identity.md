---
title: "Design Doc: macOS app identity for the graith daemon"
authors: Dougal Matthews
created: 2026-07-19
status: Accepted
reviewers: issue-1473-daemon-service, independent architecture review
informed: release maintainers, issue-1473-daemon-service
issue: https://github.com/d0ugal/graith/issues/1472
---

# macOS app identity for the graith daemon

Supported macOS packages will ship a small signed `Graith.app` that owns the
per-user daemon as an app-associated LaunchAgent. The default profile and a
finite pool of 64 named-profile slots are signed into the bundle and registered
with `SMAppService`. Jobs are registered but demand-started on the first CLI
command, never kept alive unconditionally, and always execute the daemon
payload from a validated versioned copy of the signed bundle.

## Background

`client.EnsureDaemon` first probes the profile's Unix socket. When no live
daemon answers, `internal/client/autostart.go:190-224` resolves the current CLI
executable, re-executes it as `gr daemon start`, redirects standard I/O, and
sets `Setsid`. The child is detached from the terminal's POSIX session, but
macOS still sees a command-line process launched by the terminal application.

`daemon.Run` is already a foreground blocking process. On normal startup it
acquires a profile-specific PID file, binds a profile-specific Unix socket,
loads state and the human token, and serves until SIGTERM, SIGINT, or an upgrade
request (`internal/daemon/run.go:71-370`). SIGTERM gracefully stops all managed
sessions and removes the socket and PID file. SIGHUP reloads configuration.

`gr daemon restart` preserves eligible PTY sessions by asking the running
daemon to write an identity-bearing manifest, make the listener and PTY file
descriptors survive exec, and `syscall.Exec` a replacement `gr daemon start
--adopt-from ...` in the same PID (`internal/daemon/upgrade.go:241-289`). The
request currently treats the client's `os.Executable()` path as the replacement
binary. The new generation validates the manifest profile and process
identities before adopting. Headless and unadoptable processes are terminated
rather than orphaned. A protocol-boundary restart is deliberately clean.

`GRAITH_PROFILE` is resolved before config loading. Valid names are lowercase
alphanumeric plus hyphens, at most 32 characters, and derive independent config,
data, runtime, socket, PID, state, token, log, and database paths
(`internal/config/paths.go:34-98`). An explicit `--config` selects a different
config file, and `data_dir` can relocate most state and, depending on XDG
resolution, runtime paths. The daemon and every agent currently inherit the
environment of the first CLI process that starts the daemon. Agent PTY and
headless drivers then inherit the daemon environment, with Graith's session
variables overlaid (`internal/pty/session.go:809-834`). This includes `PATH`,
credential variables, `SSH_AUTH_SOCK`, XDG variables, and other user settings.

The Darwin release is built by GoReleaser on macOS. It currently contains a
standalone `gr` plus an ad-hoc-signed `GraithNotifier.app`; Homebrew installs the
notifier in `libexec/graith`. The notifier uses macOS 11 and exists only for User
Notifications identity. The source-built `GraithGUI.app` uses macOS 14, is not
part of CLI release artifacts, and has a separate bundle identity. Neither is a
safe owner for the CLI-distributed daemon today.

Apple's macOS 13 `SMAppService` API registers LaunchAgents whose plists live in
the calling app's signed `Contents/Library/LaunchAgents` directory. Such plists
may use `BundleProgram` so launchd resolves an executable relative to the app.
Registration records the parent bundle and exposes enablement in Login Items.
The API also requires re-registration when an agent plist or executable changes.
An embedded signed plist cannot be generated once per arbitrary Graith profile.

## Problem

Closing the terminal leaves the daemon running, but its macOS ownership and
background activity remain tied to whichever terminal happened to launch the
first `gr` command. Users cannot identify or control that activity as Graith in
the normal macOS app and background-item surfaces.

Changing only the parent process or installing a static plist would not solve
the whole problem. A safe design must preserve bounded race-safe auto-start,
intentional stop, config reload, process-identity checks, live PTY adoption,
profile isolation, package upgrades, user disablement, and unbundled/older-OS
fallbacks. It must also establish application identity with observed macOS
evidence rather than infer it from a parent PID.

## Goals

- Attribute a supported package's default and named-profile daemon activity to
  Graith independently of Terminal, iTerm2, Ghostty, or another invoking app.
- Use a per-user agent in the logged-in GUI session; never install a root or
  system daemon.
- Preserve first-command auto-start and all configured dial, handshake, start,
  and poll budgets under concurrent callers.
- Keep intentional stop stopped and make user disablement fail closed instead
  of silently bypassing macOS controls.
- Preserve reload, clean restart, protocol-boundary restart, same-PID exec
  upgrade, PTY adoption, and early upgrade-failure cleanup.
- Give every profile a unique service label and retain its existing socket,
  PID, state, config, authentication, and lifecycle boundaries.
- Keep the CLI, app bundle, registered job, and replacement daemon generation
  version-aligned across Homebrew, tarball, rollback, and development installs.
- Make changed launch environment, TCC identity, config resolution, auth, and
  agent sandbox assumptions explicit and testable.

### Non-Goals

- Implementing the production service in this design issue.
- Starting a daemon merely because the user logged in, or providing continuous
  crash restart when no CLI command needs it.
- Moving daemon ownership into the unreleased macOS GUI application.
- Merging `GraithNotifier.app` or notification permission into the service app.
- Changing iOS behavior or adding mobile service controls.
- Making source builds, `go install`, or macOS 11/12 claim app-associated
  ownership when they lack the supported signed bundle/API.
- Converting the current Unix protocol to launchd socket activation or XPC.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI on macOS 13+ from Homebrew or the signed tarball | Targeted | It discovers, validates, registers, starts, stops, upgrades, and removes the bundled per-user service. |
| macOS GUI | Informed, not owner | A future distributed GUI may show service state, but the CLI-distributed bundle must work without it and remains the canonical owner. |
| iOS | Excluded | iOS is a remote frontend and cannot own a daemon on the Mac. |
| macOS 11/12 | Direct-spawn fallback | The CLI minimum is not raised; `SMAppService` is unavailable, so current detached spawning remains with an explicit doctor diagnostic. |
| Unbundled/source/`go install` on macOS | Direct-spawn fallback | There is no signed, version-matched owner bundle to register. The fallback is tested and reported, not assembled ad hoc. |
| Linux | Unchanged direct spawn | Service Management and macOS app identity do not apply. |

The packaged `Graith.app` has a macOS 13 minimum. This does not change the
standalone Go CLI or notifier minimum. The default profile does not consume a
slot. A supported package may have at most 64 simultaneously registered named
profiles, whether running or dormant; this is a service-registration limit, not
a change to valid profile names. At capacity Graith reports the mapped profiles
and removal commands, and never falls back to Terminal-owned direct spawning.

## Proposals

### Proposal 0: Do Nothing

Keep `Setsid` direct spawning on every platform. It is simple, already preserves
terminal closure, and has no packaging work. It leaves the reported macOS
identity problem unchanged and provides no app-associated background-item
control, so it does not meet the goal.

### Proposal 1: Dedicated app with demand-started associated agents (Recommended)

Ship a minimal `Graith.app` beside the Darwin CLI. Its bundle identifier is
`net.graith.service`, display name is Graith, and `LSUIElement=true` prevents a
Dock or menu-bar presence. It contains:

- a small controller executable that calls Service Management and performs
  exact-job registration and removal;
- the exact release's signed `gr` daemon payload under `Contents/MacOS`;
- one embedded LaunchAgent plist for the default profile plus 64 numbered
  named-profile slot plists; and
- ordinary version, icon, signing, and notarization metadata.

The source-built `GraithGUI.app` stays independent. It is not installed by the
CLI release, has a higher OS minimum, and would couple daemon availability to a
separate product channel. `GraithNotifier.app` also stays independent because
notification authorization and background-service ownership are different
identities and lifecycles.

The notifier's older identifier is `com.graith.notifier`, while the GUI already
uses `net.graith.macos`. The new service standardizes on the `net.graith`
namespace; renaming the notifier would break a separate notification identity
and is not part of this design.

#### Service identity and profiles

The default profile uses the signed embedded plist and
`SMAppService.agent(plistName:)`. Its launchd label is
`net.graith.service.daemon` and its `BundleProgram` is `Contents/MacOS/gr`.

`SMAppService` can only register plists already sealed inside the app. Both the
plist contents and launchd label are therefore static. Registering that job for
every named profile would conflate arbitrary daemons under one launchd job and
cannot work concurrently. The release therefore seals a bounded pool of static
agents into the same signed app:

```
default: net.graith.service.daemon
slots:   net.graith.service.daemon.profile.00 ...
         net.graith.service.daemon.profile.63
```

Every job uses `BundleProgram=Contents/MacOS/gr` and is independently registered
through `SMAppService.agent(plistName:)`. A durable lease maps one canonical
profile to exactly one numbered slot and one slot to exactly one profile. The
profile itself remains the existing validated, arbitrary lowercase name and
continues to determine socket, PID, state, config, authentication, and lifecycle
paths; a slot is only stable macOS registration identity.

The 64 slot plists, Swift controller lookup, Go allocator constant, and release
manifest are generated from one checked-in source of truth. A generation test
requires exactly 64 unique, zero-padded labels and plist names, plus the unique
default, and verifies every resource is sealed into the app. Hand-editing an
individual slot plist or maintaining duplicate label tables is forbidden.

Named-profile allocation uses one global transaction lock, not merely a lock per
job. Before registration, it atomically persists a pending lease containing the
profile, slot, UID, and app generation. Only then may it register the exact
embedded service. After interruption, reconciliation inspects the lease, the
registered generation, `SMAppService` status, and any live verified daemon and
either commits that same bijection or unregisters a proven-down job before
freeing the slot. An uncertain unregister, indeterminate PID, or live daemon
quarantines the slot; it is never reassigned. `daemon stop` leaves the lease.
Only explicit service removal releases it after the daemon is confirmed down
and the service confirmed unregistered.

The cap is 64 simultaneously registered named profiles, not 64 running daemon
processes. The 65th allocation returns an actionable error directing the user
to `gr daemon service status --all-profiles` and
`GRAITH_PROFILE=<name> gr daemon service remove`. Existing direct-spawn daemons
remain usable while live during migration, then acquire a slot on their next
managed start; a profile that cannot acquire a slot is not direct-spawned. This
is an intentional availability cliff rather than a misleading partially managed
mode: bypassing the cap would also bypass Graith ownership and macOS disablement.
`gr doctor` warns once 56 slots are leased and every status/capacity error lists
the dormant leases that can be removed. A clean stop/restart of a live unmanaged
profile first reserves a slot; if none is available, it cancels before stopping
the working daemon. An unexpected exit at capacity can still leave the profile
unavailable until a lease is explicitly removed, which migration documentation
calls out.

No agent sets `RunAtLoad`, `KeepAlive`, `Sockets`, or a Mach service. Login
loads registered definitions but starts no daemon. The first CLI command writes
startup intent and explicitly kickstarts the job. A crash remains down until
the next command, matching current demand recovery and avoiding an unconditional
`KeepAlive` undoing `gr daemon stop`.

All supported packaged profiles therefore use the same empirically verified
modern registration and signed app association. macOS 11/12 retain direct spawn
instead of installing a new legacy persistence mechanism.

#### Install discovery and immutable generations

Darwin archives retain the standalone `gr` for older macOS and unbundled use,
and add `Graith.app`. Homebrew installs `gr` in `bin` and the app in
`libexec/graith`; the CLI knows both layouts. Stable Darwin release jobs require
provisioned Developer ID Application identity, keychain, and notarization
credentials. Each architecture's `gr` must exist before its matching app is
assembled and nested-code-first signed; GoReleaser then archives that completed
per-architecture app. The pipeline enables hardened runtime without App Sandbox,
notarizes the final bundle, staples the ticket, and verifies the archive after
copying. Missing credentials, signing, notarization, stapling, app resources, or
payload parity fail the stable release: it may not silently substitute ad-hoc or
unsigned code or omit the app. Ad-hoc signing is only for explicitly labelled
local/development artifacts and this design spike.

On first supported use, the CLI validates the discovered app's signature,
bundle identifier, designated requirement/team, embedded version, and payload
hash before copying the untouched signed bundle to a per-user versioned cache:

```
<OS-user-home>/Library/Application Support/Graith/services/
    <version>-<commit>/Graith.app
```

The OS user home comes from the user database for the effective UID, never from
`HOME` in the caller or launchd environment. The precise cache root is
independent of `data_dir` and profiles because it owns executable generations,
not daemon state. It is user-owned mode 0700; no component is accepted through
a symlink or writable-by-group/other ancestor.
The registered generation is retained until no job references it. Package
manager cleanup therefore cannot remove the executable launchd still knows,
and installing a new release never mutates a running generation in place.

The release build tests that standalone and embedded `gr` have the same bytes
before signing and report the same version/commit after signing. It runs strict
`codesign`, Gatekeeper, and staple validation on the assembled app, in each
archive, and after installation into the cache. Homebrew and tarball packaging
tests fail when the app, generated slot set, signature, staple, or expected
payload is absent. The macOS-built `dev` release uses the same Developer ID
requirement and notarization gate as stable Graith, then registers only its
isolated `dev` profile. Globally unique bundle build numbers prevent stable and
dev generations from becoming ambiguous in the shared service receipt. Source
`make build` remains the explicit direct-spawn development path.

#### Startup intent and environment

launchd does not inherit the terminal environment, while current behavior makes
the first successful starter's environment the daemon and agent base
environment. Production plists contain no persistent `EnvironmentVariables`.
Instead their immutable arguments identify only their trusted entry point:

```
default: gr daemon start --internal-service-label=net.graith.service.daemon \
            --internal-service-slot=default
slot 00: gr daemon start \
            --internal-service-label=net.graith.service.daemon.profile.00 \
            --internal-service-slot=00
```

The hidden marker is parsed in `executeWithArgs` before Cobra's
`PersistentPreRunE` resolves profile paths or loads config, just as the current
`--adopt-from` failure guard is armed early. A marker-free manual
`gr daemon start` and the older-OS direct-spawn path retain their current flow.
Before kickstart, an eligible human CLI writes one short-lived request per
service label under `<OS-account-home>/Library/Application Support/Graith/services/control/bootstrap`.
The account is resolved for the effective UID with `os/user.LookupId`; the
returned UID must match, and its clean absolute non-root home must be an
effective-user-owned, non-symlink directory. The service, control, and bootstrap
directories are owner-only. Request creation and consumption use
descriptor-relative no-follow operations and fail closed on every ownership,
mode, type, or path mismatch. Generation caches and durable receipts are
protected siblings under the same `services` root. The root is never taken from
the request, `HOME`, `TMPDIR`, a Darwin temporary-directory helper, XDG/profile
data or runtime paths, or config:

```
schema version, profile, slot, label, config file, resolved Paths,
bundle generation, projected environment, nonce, caller UID,
creation/expiry time
```

The directories are mode 0700 and requests are atomically published mode 0600 with
no symlink traversal. The early bootstrap accepts a request exactly once and
only for its supported schema, current UID, immutable label/slot marker,
validated cached generation, nonce, and short freshness window. For a named
profile, the request, durable lease, registered generation, slot, and canonical
profile must all agree; the default marker must resolve only the default
profile. Only after those checks may the bootstrap install the profile, config
path, and projected environment and let normal config loading continue. It
consumes and unlinks the request on success; all failure and timeout paths
remove it without binding a socket or reading daemon state.

The CLI service launcher, not the Service Management controller, refuses to
create a request when it detects Graith session identity or an agent/hook
caller. An existing daemon remains connectable from an agent, but an agent may
not become the first managed starter. Independently, the early service
bootstrap rejects reserved variables and any request/lease mismatch even if a
caller bypasses the CLI check. Graith-generated safehouse and nono policies
never grant the control tree or an enclosing directory. An exact execution/read
grant for a cached `Graith.app/Contents/MacOS` path does not grant its sibling
control tree. An operator can still expose it by disabling sandboxing or
explicitly granting a broad home path; Graith never adds that exposure
automatically.

After any needed profile allocation under the global transaction, concurrent
starters serialize through a per-label start lock. The first eligible
creator fixes config, paths, environment, and generation for that start attempt.
Later callers never overwrite the request: they wait and probe the same socket
within `EnsureDaemon`'s existing aggregate budget. If the winner fails and
removes its request, one waiter may acquire the lock and retry with its own
contract.

The request does **not** copy `os.Environ()`. UID, user name, `LOGNAME`, and
`HOME` are derived afresh from the OS account and cannot be projected. Its
built-in projection is limited to validated `PATH`, `SHELL`, `TMPDIR`, locale
variables, and absolute XDG base-directory overrides needed to reproduce path
and tool resolution. Before any registration or request mutation, the launcher
rejects a `TMPDIR` whose canonical path is the protected services tree, an
enclosing directory, or a symlink to either; generated sandbox base policies
must not turn inherited temporary-directory access into service-state access.
A new `[daemon_service].inherit_env` list lets a user
explicitly opt additional names such as `SSH_AUTH_SOCK` or an agent API key into
the daemon/agent base environment. Documentation calls out that this grants the
service and eligible agent processes access to those values.

This is a deliberate macOS 13+ packaged-install migration. Credentials and
socket endpoints that agents currently inherit implicitly, including
`SSH_AUTH_SOCK` and API tokens, disappear unless they are explicitly opted in.
The user documentation, upgrade notes, and `gr doctor` show the effective
projection before enablement and list common variables without printing values.
macOS 11/12, unbundled macOS, and Linux keep the full direct-spawn environment:
tightening those fallbacks would be a separate cross-platform behavior change,
so the same configuration can intentionally produce different agent
environments on managed and unmanaged installations.

`GRAITH_SESSION_ID`, `GRAITH_SESSION_NAME`, `GRAITH_TOKEN`,
`GRAITH_WORKTREE_PATH`, `GRAITH_REPO_PATH`, `GRAITH_AGENT_TYPE`, every other
Graith session-only variable, dynamic-loader variables (`DYLD_*`, `LD_*`), and
launch-service variables (`XPC_*`, `__CF*`) are reserved and rejected even when
listed. The canonical profile is installed from the request, never inherited.
This is an intentional tightening from direct spawn: arbitrary terminal secrets
no longer flow into every future session without an explicit setting. Any
opted-in values are still sensitive, so the request is never logged or retained
as durable service state. The bootstrap clears launchd's inherited environment,
installs the OS-derived identity plus the validated projection, and removes any
reserved names before config or agent process setup. Opted-in values do touch an
short-lived owner-only file below the protected OS-account-home service control
directory until it is consumed and unlinked. macOS offers no secure-delete
guarantee, so documentation treats that disk exposure as part of the explicit
opt-in rather than claiming it is equivalent to an inherited environment.

An explicit `--config` is resolved to a clean absolute path before it is recorded
outside an agent session. The early parser installs that validated path into
CLI startup state before `PersistentPreRunE`; the daemon loads it, applies
`data_dir`, and then verifies that its derived config, data, runtime, socket,
PID, token, and
database paths equal the request. A mismatch fails before binding or reading
state. As today, once a profile daemon is running, a later client cannot change
its startup config by passing a different path; it must reload supported fields
or restart intentionally.

#### Auto-start and user controls

`EnsureDaemon` keeps its probe-first shape and all configured timeouts. Only the
platform launcher changes:

1. Probe and connect to an existing profile daemon.
2. On supported packaged macOS, validate/cache the matching app generation and
   resolve the default label or durable named-profile slot lease.
3. Inspect the exact `SMAppService` status. If it is `notRegistered` or
   `notFound`, allocate if needed and register from the validated app; the
   prototype observed both initial states before successful registration. If
   macOS reports `requiresApproval`, user disablement, or registration fails,
   return an actionable error and never bypass the choice with direct spawning.
4. Write startup intent, kickstart the exact `gui/<uid>/<label>` job, and poll
   the normal socket within the existing start budget.
5. On older macOS or when no valid bundle exists in an unbundled installation,
   use the existing `Setsid` direct launcher and report that fallback in
   `gr doctor`.

A packaged app that is present but invalid, version-mismatched, denied, or at
slot capacity is an error rather than an unbundled fallback.
`gr daemon service status` reports the
profile, label/slot, lease state, registered and running generations, macOS
status, fallback mode, and remediation. `--all-profiles` reports all leases and
quarantined slots. A separate service-settings command may open Login Items
only after explicit user confirmation.

#### Lifecycle contract

| Operation | Managed behavior |
|-----------|------------------|
| First ordinary CLI command | The client registers or reconciles the dormant job, writes startup intent, kickstarts it, and uses the existing bounded readiness probe. |
| Internal service `daemon start` payload | launchd invokes the foreground daemon exactly once with the trusted marker. It consumes bootstrap state and enters `daemon.Run`; it never calls `EnsureDaemon` or recursively kickstarts itself. |
| `daemon stop` | Identify the daemon from the authenticated Unix peer/PID identity, send SIGTERM, wait for that identity and socket to disappear, and leave the job registered but dormant. No restart occurs. |
| `daemon restart` | Prepare the existing manifest and `exec` a validated newer bundled payload in the same PID. The launchd job remains active around exec and PTY adoption keeps its current fail-closed checks. |
| `daemon restart --force` | Stop the exact process and its sessions, reconcile registration while down, write fresh intent, and kickstart. |
| Protocol-boundary restart | Gracefully stop the exact old process and sessions, prove identity/socket disappearance, reconcile registration, then kickstart the new generation. |
| `daemon reload` / SIGHUP | Use the existing in-process config transaction. If down, the command starts the service with the requested startup config, as today. |
| Unexpected daemon exit | Stay down because there is no `KeepAlive`; the next CLI command reconciles and starts it. Existing state/orphan cleanup semantics apply. |
| Logout/reboot | launchd ends the GUI agent. On next login the definition is loaded but dormant; the first CLI command starts it. |
| Disable in Login Items | macOS may stop/unload it; every later CLI start fails with approval guidance and never direct-spawns around the setting. |
| Service removal | Stop the exact daemon, unregister each selected `SMAppService`, release named-profile leases only after confirmed unregister, and remove unreferenced cached apps, but preserve config, state, worktrees, tokens, and logs. |

`daemon stop` is not uninstall and does not alter user approval. A later ordinary
command starts again by design. Uninstall is explicit because silently removing
registration on every stop would discard the user's background-item decision
and make upgrade reconciliation racy.

The daemon stays in the foreground from launchd's perspective. The managed path
must not call `Setsid`, fork a supervisor, double-fork, or spawn a child daemon.
The service payload remains named `gr`, so current `IsGraithDaemon` and PID
identity checks do not weaken to accept a generic helper name. Session processes
keep their existing PTY/process-group behavior.

#### Preserved upgrades and registration rotation

The managed path may no longer trust any client-supplied executable merely
because it exists. Before sending an upgrade, the new CLI installs its signed
app in the versioned cache. The old daemon accepts the candidate only when the
real path is the embedded `gr` of a secure cached bundle, the Developer ID/team
and designated requirement match the registered owner, the bundle and payload
verify, and the embedded version/commit equal the request. Unmanaged fallback
retains current executable resolution.

The old daemon then uses the existing listener/session manifest and same-PID
exec. `ExecUpgrade` carries the internal managed label/slot marker as well as
`--adopt-from`. Because the one-shot request was already consumed, the early
upgrade path validates the marker against the manifest, current verified PID,
durable profile lease, and generation receipt rather than requiring a new
request. The new daemon inherits the projected environment, profile, config
path, listener, and PTYs. Its early guard, profile comparison, persisted process
identity checks, adoption rollback, generation ID readiness, and protocol
boundary remain unchanged. A marker without either a valid fresh request or a
valid same-PID adoption manifest fails before config loading.

Apple requires re-registration after the service executable changes, but
unregistering a running agent kills it and would destroy the just-preserved
handoff. The no-`KeepAlive` design separates process and registration rotation:

- the running PID may exec the validated new cached generation;
- the receipt records `running_generation != registered_generation`, profile,
  and stable slot lease, and retains both immutable apps;
- no registration mutation occurs while that PID is live; and
- after stop, crash, logout, or before the next clean start, Graith invokes the
  validated controller from the *registered* cached generation to unregister
  the old dormant job, invokes the candidate controller to register the same
  default/slot plist in the new app, updates the receipt atomically without
  changing the profile-slot mapping, and only then removes an unreferenced
  cache.

Rotation uses a strict fresh-registration operation: an enabled or concurrently
reappearing label is an error, never evidence that the candidate owns it. The
receipt advances only after launchd reports the expected Graith parent bundle,
bundle build, and daemon program identity for the candidate generation.

If new registration fails, the retained old controller restores the old dormant
definition and Graith returns an error without starting either generation. A
failed restoration leaves the slot quarantined and preserves both caches; it is
never freed or direct-spawned. The next CLI command can retry or report repair
steps. Because no job is kept alive or run at login, launchd cannot race the
rotation by relaunching the stale program. This also makes rollback symmetric:
a compatible older CLI installs its signed cached bundle and rotates only while
down; the existing state-version guard still prevents a downgrade from reading
newer state destructively.

#### Security, privacy, and sandbox assumptions

All jobs run as the logged-in user in `gui/<uid>`, never as root. Existing Unix
peer credentials, human/session tokens, profile handshakes, remote auth, file
modes, and sandbox checks remain authoritative. Service labels and receipts are
discovery/lifecycle metadata, not authentication.

The service app is hardened-runtime signed but not App Sandbox entitled: the
daemon must manage PTYs, subprocesses, repositories, Unix sockets, and configured
tools. Graith's per-agent safehouse/nono boundary is unchanged and still fails
closed. No request or receipt can relax an agent sandbox.

Implementation review found that the originally accepted temporary-directory
control root was automatically writable by the same-UID agent sandboxes on
macOS. That contradicted this record's protected-bootstrap boundary. The
2026-07-20 accepted amendment replaces all `DARWIN_USER_TEMP_DIR`/`getconf`
language with the OS-account-home sibling layout above and requires generated
policy plus real macOS enforcement tests for read, modify, delete, and replace.

Moving launch identity away from Terminal also moves macOS privacy attribution.
Terminal Full Disk Access or Automation grants must not be assumed to carry to
Graith. Protected repository, keychain, notification, Apple Events, or other TCC
access may require a Graith-specific grant or may fail. Startup and doctor report
those failures under Graith identity; they never fall back to a terminal-owned
daemon to inherit broader permission. The projected environment preserves path
resolution and any explicitly opted-in socket such as `SSH_AUTH_SOCK`, but it
does not manufacture authorization.

#### Installation, removal, and rollback

Homebrew installs but does not start/register the service during `brew install`;
registration needs a logged-in user context and follows first use. Homebrew's
Formula DSL has no supported pre-uninstall hook, so the formula does not pretend
to remove jobs automatically and never removes them during upgrade. Its caveats
require `gr daemon service remove --all-profiles` before `brew uninstall` and
link exact recovery steps if that was missed. Reinstalling a matching or newer
signed package makes `gr daemon service repair` available for orphan diagnosis;
raw wildcard `launchctl` cleanup is never recommended.

Tarball users keep `Graith.app` beside `gr`; first use copies the signed app to
the cache. Documentation gives the same explicit removal command. Deleting the
tarball directory does not break a registered generation because it runs from
the cache. Removing service registration never deletes user state.

At least the previous registered cache is retained through upgrade and rollback.
Garbage collection removes only bundles not referenced by a registration,
profile lease, running verified PID, pending transaction, or primary/backup
receipt. It fails closed on an unknown, unsigned, or indeterminate reference.

The lease/registration receipt is a versioned, checksummed, owner-only file
updated by write-fsync-rename under the global allocation lock. The previous
valid generation is retained as an atomic backup. Both record the default
registration, all profile-slot bijections, pending operations, registered and
running app generations, and monotonically increasing transaction generation.
Corruption or loss never causes an empty map to replace known ownership.

The receipt and its previous generation are deliberately the only authoritative
profile-to-slot sources because a static signed plist cannot contain a dynamic
profile. If both are deleted or unusable, dormant slots cannot be reconstructed
from launchd metadata and may all be quarantined. This recoverability cost is a
known trade-off of sealed slots; repair favors keeping unknown sessions safe
over automatic availability, and tests cover simultaneous loss of both copies.

`gr daemon service repair` first tries the valid backup, then enumerates only
the 65 compiled exact labels and securely enumerates controller-owned cached
apps. For every candidate it validates directory ownership/modes, app signature,
team and bundle identity, embedded plist label, Service Management status, and
any process/socket/PID identity before acting. A proven-down dormant orphan may
be unregistered and its slot freed. An unknown live daemon, indeterminate PID,
disabled/uncertain registration, or mapping that cannot be reconstructed is
reported and quarantined; repair does not kill sessions merely to recover a
profile name. Removal and repair never search by prefix, pass wildcard launchd
targets, or accept a caller-supplied label. The default and all slots remain
separate explicit entries.

### Proposal 2: Make the existing GUI app the owner

Embed the agent in `GraithGUI.app` and let the GUI register it. This avoids a
second Graith-named app bundle. It loses because the GUI is not shipped with the
CLI/Homebrew artifacts, currently targets macOS 14, has an independent release
and notarization track, and should not be required for a terminal-first tool.
Future GUI builds can consume the service controller without becoming its
package owner.

### Proposal 3: One static `SMAppService` for every profile

A single service could act as a broker and spawn one child daemon per profile.
This keeps only modern registration, but launchd would supervise the broker
rather than each foreground daemon, one label would conflate profile lifecycle,
and a new privileged control plane would be required to transfer environments,
config, sockets, upgrades, and stop intent. It violates the existing foreground
daemon invariant and is much larger than the bounded static slot pool.

## Consensus

The implementation owner approved the architecture after reviewing the actual
auto-start, config/profile, lifecycle, adoption, and packaging constraints. The
review replaced an earlier modern-default/dynamic-legacy hybrid with the signed
64-slot pool because a generated legacy plist's app attribution could not be
established. It also changed full environment capture to an explicit projection,
added the fixed pre-config bootstrap, made profile allocation a global
crash-recoverable transaction, retained old controllers through registration
rotation, corrected Homebrew Formula removal semantics, and made stable release
signing fail closed.

Independent review verified the cited current behavior and found no remaining
architecture blocker. Its robustness findings made the managed/unmanaged
environment divergence, 64-slot availability cliff, total-receipt-loss
quarantine, and transient secret-file exposure explicit. It also caused the
spike to use nested-code-first signing, omit a fixed `PATH` and predictable log
paths, document lifecycle reproduction, and script its two-copy controller
check. The lifecycle table now separates the ordinary CLI launcher from
launchd's internal foreground payload so `daemon start` cannot recurse through
`EnsureDaemon`.

The #1473 implementation tribunal then identified the temporary-root sandbox
contradiction. The #1472 design owner confirmed the OS-account-home correction
recorded above as faithful to the architecture, not a mechanism change. This
amendment supersedes the earlier temporary-root, transient-temp-secret,
`getconf`, and associated test-matrix language.

Reviewers disagreed only on whether missing visual evidence should block design
acceptance. This record accepts the architecture on the captured
Service Management parent-bundle and independent-job evidence, but does **not**
claim an Activity Monitor result. A signed production candidate may not be
enabled or #1473 completed until manual Activity Monitor and Login Items checks
show Graith ownership for the default and two concurrent named slots. A contrary
result invalidates the central premise and returns this record to Draft rather
than permitting a direct-spawn fallback.

## Other Notes

### References

- [Issue #1472](https://github.com/d0ugal/graith/issues/1472) and implementation
  issue [#1473](https://github.com/d0ugal/graith/issues/1473).
- [Apple `SMAppService`](https://developer.apple.com/documentation/servicemanagement/smappservice)
  — signed app helper registration and status.
- `SMAppService.h` in the macOS SDK — embedded plist rules, immediate per-user
  bootstrap, user approval, update re-registration, and unregister kill behavior.
- `launchd.plist(5)` — `BundleProgram`, `KeepAlive`, session type, and process
  classification.
- `internal/client/autostart.go` — current probe, bounded start, and `Setsid`.
- `internal/cli/daemon.go` — stop, reload, preserve/clean restart, and generation
  readiness behavior.
- `internal/daemon/run.go` and `internal/daemon/upgrade.go` — foreground daemon,
  signal loop, manifest, same-PID exec, and failure cleanup.
- `internal/config/paths.go` and
  `docs/design/2026-06-10-graith-profiles-design.md` — profile/path isolation.
- `.goreleaser.yaml`, `.goreleaser-dev.yaml`, and
  `macos/notifier/build.sh` — current Darwin, development, Homebrew, and notifier
  packaging/signing behavior.
- `docs/design/spikes/1472-macos-daemon-identity/` — reproducible disposable
  prototype source.

### Implementation Notes

Implementation issue #1473 has independently shippable, ordered phases, but no
phase is the complete feature by itself:

1. Build, sign, notarize, package, discover, validate, and cache `Graith.app`;
   generate and verify all embedded agents, then add the controller and read-only
   service status. Direct spawn remains until lifecycle integration lands.
2. Add the fixed-root early bootstrap, protected startup intent and environment
   projection, default-profile registration, demand kickstart,
   stop/restart/reload integration, disabled-service handling, and managed exec
   validation.
3. Add the global profile-slot allocator, durable/backup receipts and repair,
   registration rotation/rollback, explicit removal, cache GC, doctor/user docs,
   and the full Homebrew/tarball/platform matrix.

The #1473 issue body or a linked checklist must track these phases explicitly.
Production enablement is not complete until all three pass; partial phases stay
behind an internal build gate and must not switch `EnsureDaemon` to a path that
manages only the default profile or lacks safe removal/repair.

Every static plist omits `KeepAlive`, `RunAtLoad`, socket activation, and
persistent environment values. Slot resources are generated from the shared
manifest and validated with `plutil` before signing. Launchd calls use exact
`gui/<uid>/<label>` targets; no wildcard stop/removal is allowed.

The startup request is a new pre-config bootstrap boundary similar in risk to
the early upgrade guard. Parsing, ownership/mode validation, environment install,
config/path verification, and guaranteed cleanup should be isolated and unit
tested before `daemon.Run`. The design spike deliberately does not prototype
this security boundary, so phase 2 cannot enable it until its failure-mode and
race tests pass. Slow Service Management, codesign, copy, launchctl, or
filesystem work must not occur under the session-manager lock.

### Alternatives considered

- **Unconditional `KeepAlive`:** rejected because it immediately undoes an
  intentional stop and complicates upgrade registration. Demand recovery on the
  next CLI command matches the existing product contract.
- **Start at login / `RunAtLoad`:** rejected because Graith has no work until a
  command needs it and it would race stale registrations after upgrades.
- **Launchd socket activation:** rejected because profile and `data_dir` socket
  paths are dynamic, the daemon currently owns socket cleanup, and PTY adoption
  still needs same-PID exec.
- **Dynamic legacy plists for named profiles:** removes the 64-registration cap,
  but gives up sealed `BundleProgram` resources and the spike could not establish
  app association for a generated plist. The static pool keeps every supported
  profile on the mechanism whose parent-bundle association was observed. The cap
  bounds signed resources and reconciliation cost; labels are append-only so a
  later design may raise it without remapping existing leases.
- **Direct-spawn named profiles:** rejected on supported packages because it
  would leave exactly the Terminal attribution and lifecycle gap the design is
  meant to close.
- **Raise every Darwin artifact to macOS 13:** rejected; the standalone CLI and
  notifier can continue serving macOS 11/12 with explicit fallback semantics.
- **Mutate one stable cached app in place:** rejected because it can invalidate
  a running signature/path and contradicts Apple's re-registration requirement.
- **Trust `UpgradeMsg.ExecPath`:** rejected for managed jobs; an app-associated
  service must not exec out of its validated owner bundle.

### Prototype evidence

On 2026-07-19 an arm64 Mac running macOS 26.5.2 built the disposable spike in
`docs/design/spikes/1472-macos-daemon-identity/`. The ad-hoc-signed `Graith.app`
contains the real current `gr`, a tiny `SMAppService` controller, and three
embedded agents modelling the default and slots 00 and 01. None has `RunAtLoad`
or `KeepAlive`. The spike uses static `GRAITH_PROFILE` values only to isolate its
three real daemons; the production environment/request bootstrap is deliberately
not implemented here.

Signature verification succeeded:

```
Identifier=net.graith.design-spike
Format=app bundle with Mach-O thin (arm64)
Signature=adhoc
.../Graith.app: valid on disk
.../Graith.app: satisfies its Designated Requirement
```

Initial controller statuses were `not-registered`, `not-found`, and `not-found`.
Registering each independently returned `enabled`. After three exact
`launchctl kickstart -kp` calls, `launchctl print` showed distinct running jobs:

```
label                                          pid    managed_by
net.graith.design-spike.daemon                 29889  ServiceManagement
net.graith.design-spike.daemon.profile.00      30202  ServiceManagement
net.graith.design-spike.daemon.profile.01      30384  ServiceManagement

state = running
program identifier = Contents/MacOS/gr (mode: 2)
parent bundle identifier = net.graith.design-spike
parent bundle version = 1
runs = 1
last exit code = (never exited)
```

Unregistering only slot 00 changed its status to `not-registered` and removed
only its launchd job. The default and slot 01 remained `enabled`, running with
their original PIDs, and the same parent-bundle metadata. Re-registering slot 00
returned `enabled` and kickstarted distinct PID 30602. Sending SIGTERM to slot
01 then left it loaded but `state = not running`, `runs = 1`, and
`last exit code = 0` after two seconds, demonstrating that no unconditional
restart undid the stop.

Final cleanup called `unregister` for slot 01, slot 00, and the default. All
three controllers immediately reported `not-registered`; two launchd records
remained briefly visible and all three exact `launchctl print` targets were
absent after two seconds. Production removal therefore waits for both process
exit and job disappearance before releasing a lease or cached generation; the
API return/status alone is not treated as synchronous cleanup proof.

`lsappinfo info` returned null name, bundle identifier, bundle path, and app PID
fields for the live default and slot processes. That is useful negative evidence:
the headless agent does not masquerade as a foreground application. The positive
command-line attribution evidence is Service Management's parent bundle on all
three independently controlled jobs. Parent PID and plist contents were not used
as substitutes for observed launchd state.

A second copy/sign test, reproducible with the committed `cross-copy.sh`,
registered version 1 from one app path, then queried and unregistered it using a
version 2 copy with the same bundle identifier at a different path. The new
controller saw `enabled`; after its unregister call, both controllers reported
`not-registered` and the launchd service was absent.
This proves cross-copy control on the current OS, but the production design still
retains and invokes the registered generation's validated controller so it does
not depend on undocumented cross-version behavior.

The required manual Activity Monitor observation is pending because the agent
environment is denied Screen Recording and Accessibility access. It remains an
explicit manual acceptance item for #1473; the launchd metadata alone is not a
substitute for the production signed-release check. That check covers the
default and two concurrent named slots in Activity Monitor and Login Items.

### Testing

Unit and focused tests in #1473 must cover:

- bundle discovery for Homebrew, tarball, missing bundle, tampered signature,
  wrong team/bundle/version/hash, symlink, insecure ancestor, and stale cache;
- generated resource completeness and uniqueness for the default and all 64
  label/plist/controller/allocator entries, with no `KeepAlive`, `RunAtLoad`, or
  persistent environment;
- the global allocator's profile-slot bijection, 64-registered-profile capacity,
  pending-before-register crash recovery, stop-retained leases, concurrent
  allocation, safe release, and never-direct-spawn capacity failure;
- primary/backup receipt atomicity, schema/checksum failures, registered/running
  generation references, simultaneous loss of both copies, repair from backup,
  dormant-orphan recovery, and quarantine of live or indeterminate unknown
  slots;
- service states not registered, enabled, requires approval, disabled, missing,
  stale, and registration rollback failure;
- pre-config marker parsing for fresh starts and same-PID adoption; fixed
  OS-account-home `services/control/bootstrap` root; effective-UID account,
  home ownership/type/mode and no-follow failure modes; startup request schema, lease agreement,
  ownership, permissions, symlink rejection, expiry, nonce, one-creator
  contention, environment projection/reserved-name rejection, secrecy/cleanup,
  custom `--config`, XDG paths, and `data_dir`;
- concurrent first clients, stale/foreign sockets, stuck handshakes, and exact
  preservation of configured dial/handshake/start/poll budgets;
- stop staying dormant, force restart, reload-while-down, crash then demand
  recovery, logout/login dormancy, disabled-service non-bypass, and uninstall;
- preserve exec of a valid new cached generation, rejection of arbitrary client
  paths, same-version generations, registration rotation only while down, old
  bundle retention, failed rotation rollback, state-version downgrade guard,
  and a macOS test that the registered old job tolerates same-PID exec into the
  validated new cached payload until later down-state rotation;
- profile separation across labels, requests, socket, PID, state, config,
  auth/token, stop/restart, and concurrent default/profile daemons;
- agent sandbox behavior and the service environment contract: built-in
  projection, explicit `SSH_AUTH_SOCK`/credential opt-in, reserved variables,
  no automatic grant of `services/control` or an enclosing path, real macOS
  read/modify/delete/replace denial, and no request or log leakage; and
- Homebrew/tarball install, upgrade, rollback, removal, notarization/stapling,
  exact payload parity, older macOS, unbundled fallback, and unchanged Linux
  archives.

The integration/release matrix is:

| OS/install | Profiles | Required evidence |
|------------|----------|-------------------|
| Current macOS, signed Homebrew | default + two named | Three unique `SMAppService` jobs; lifecycle, upgrade, disabled state, explicit removal, Activity Monitor, Login Items, no Dock/menu item. |
| Current macOS, signed tarball | default + named | Discovery/cache, removal after source directory deletion, upgrade/rollback, attribution. |
| macOS 13 minimum | default + named | API availability, registration approval/status, demand kickstart, custom config/data dir, profile isolation. |
| macOS 11/12 | default + named | No service API call; explicit direct-spawn fallback and unchanged CLI/notifier behavior. |
| Current macOS source/`go install` | default + named | Explicit tested direct fallback; no generated unsigned production app. |
| Development release | isolated named profile | Developer ID-signed matching app from the macOS dev channel; no collision with stable default. |
| Linux amd64/arm64 | default + named | Existing direct start/stop/restart and release artifacts unchanged. |

Run focused client/CLI/daemon/release tests under `-race`, tagged lifecycle
integration tests on macOS, `go test ./...`, `go vet ./...`, Darwin lint, release
snapshot validation, codesign verification, Gatekeeper/notarization assessment,
and the Hugo documentation build. Manual testing closes Terminal after first
start, verifies the daemon remains healthy, and records Activity Monitor and
Login Items for the default plus two named slots before and after disable/remove.
The macOS 13 minimum test also proves registration rotation through the retained
old controller; cross-generation controller compatibility is recorded but not
required for correctness.
