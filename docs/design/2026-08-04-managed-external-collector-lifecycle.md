---
title: "Design Doc: Managed External Collector Lifecycle"
authors: Codex
created: 2026-08-04
status: Draft
reviewers: (none yet)
informed: obs-epic-orchestrator
issue: https://github.com/d0ugal/graith/issues/2047
---

# Managed External Collector Lifecycle

Graith should only manage an external collector after it can own the generated
config, service definition, state receipt, and diagnostics without owning Alloy
distribution. The recommended future path is an explicitly enabled
per-user service that runs an already installed Alloy binary: a Graith-owned
LaunchAgent on macOS and a Graith-owned `systemd --user` service on Linux.

## Background

Graith already has an opt-in observability contract. Logs stay local by
default, metrics bind a local Prometheus endpoint only when
`[telemetry.metrics] enabled = true`, and traces export through OTLP only when
`[telemetry.tracing] enabled = true`. The current user docs show how an external
Grafana Alloy process can tail Graith log files, scrape metrics, receive OTLP,
and forward to Loki, Mimir, and Tempo, but Graith does not start that process.

The observability reliability epic keeps that boundary intentional: generated
collector config and doctor checks should land before lifecycle management, and
no telemetry should ship by default. Issue #2047 asks for a fresh design before
Graith attempts to manage a collector service, because the previous macOS Alloy
LaunchAgent experience was unreliable.

Current upstream behavior matters here. Grafana documents macOS Alloy as a
Homebrew install that runs through `brew services`, Linux Alloy packages as a
system `alloy.service`, and standalone Alloy as a foreground `alloy run`
process that can be wrapped by systemd. Alloy reports anonymous usage
statistics by default unless `--disable-reporting` is passed. Homebrew services
can manage either launchd or systemd services, but its own service files and
registration are Homebrew-owned. systemd user services have a standard user
unit load path under `$XDG_CONFIG_HOME/systemd/user` or
`~/.config/systemd/user`, and `systemctl --user enable` wires units into the
calling user's manager.

## Problem

The manual Alloy setup is easy to misconfigure: paths depend on Graith profile
and data-dir resolution, session scrollback export has sensitive-content risk,
metrics require a restarted Graith daemon, tracing protocol and endpoint must
match the collector receiver, and Alloy itself needs service-specific storage,
credentials, and restart behavior.

Letting users keep owning every collector detail preserves safety, but it also
means Graith cannot reliably diagnose config drift, service crashes, package
upgrades, or stale generated config. At the other extreme, bundling or installing
Alloy would make Graith responsible for another project's release, security, and
platform lifecycle. A safe middle has to be explicit about which parts Graith
owns and which parts remain external.

## Goals

- Keep collector lifecycle management disabled until the user explicitly opts in.
- Manage only user-scoped services; never install a root or system collector.
- Use an already installed Alloy binary and validate it before starting or
  restarting a managed service.
- Own generated config, service definitions, secret projection, receipts,
  diagnostics, port allocation, and storage paths so Graith can detect drift
  and recover safely.
- Define macOS and Linux behavior separately instead of projecting one service
  model onto both platforms.
- Define upgrade, rollback, config drift, crash recovery, storage, and uninstall
  behavior before implementation.
- Disable Alloy's own anonymous usage reporting in any Graith-managed service.
- Preserve the existing rule that Graith sends no logs, metrics, or traces by
  default.

### Non-Goals

- Implementing the service manager in this design issue.
- Installing, upgrading, or uninstalling the Alloy package manager artifact.
- Bundling Alloy in Graith releases.
- Resurrecting the old high-availability LaunchAgent assumptions or a custom
  supervisor loop.
- Managing the Linux system `alloy.service` installed by Grafana packages.
- Supporting Windows, Docker, Kubernetes, or remote hosts in this lifecycle.
- Enabling Graith metrics, tracing, session-log export, or backend credentials
  automatically.
- Direct Loki or Mimir shipping from `graithd`.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI on macOS | Targeted | The CLI can validate Alloy, render config, install/remove a per-user LaunchAgent, and report service health. |
| CLI on Linux with systemd user manager | Targeted | `systemd --user` gives a per-user supervisor without touching the package-owned system `alloy.service`. |
| CLI on Linux without systemd user manager | Targeted | Graith can still render config and doctor checks, but should not invent another lifecycle backend. |
| macOS GUI | Excluded | A future app can display health, but the CLI remains the lifecycle authority for this lifecycle. |
| iOS | Excluded | iOS is a remote frontend and cannot own a local collector service. |
| Windows | Excluded | Alloy has a Windows service model, but Graith has no Windows daemon/service lifecycle today. |

On both macOS and Linux, managed collection remains per local OS user and per
Graith profile. A user who wants collection while logged out must configure the
platform's user-session persistence explicitly; Graith should not run
`loginctl enable-linger`, install a LaunchDaemon, or escalate privileges.

## Proposals

### Proposal 0: Do Nothing

Keep the current documentation-only Alloy example. Graith would continue to
provide optional metrics and tracing endpoints, local logs, and manual
troubleshooting guidance, but it would not generate or manage any collector
service.

This is safe and requires no code, but it leaves the reliability epic without a
way to repair stale config, notice a stopped collector, or tie diagnostics to
the resolved Graith profile. It also keeps platform-specific service details in
user-maintained notes rather than in a Graith-owned contract.

### Proposal 1: Generated Config Only

Graith renders an Alloy config and maybe an environment-file template, validates
the config with `alloy validate`, and prints exact manual commands for the user
to run. It never writes a LaunchAgent, systemd unit, Homebrew service file, or
package-manager state.

This should be the first implementation slice for the epic. It solves resolved
paths, profile selection, config validation, docs drift, and doctor checks
without crossing into service ownership. It does not satisfy a full lifecycle:
crash recovery, service restart, upgrade detection, rollback, and uninstall all
remain manual.

### Proposal 2: Homebrew Services Own the Collector

Graith could require macOS users to install `grafana/grafana/alloy` and then
drive `brew services start|restart|stop grafana/grafana/alloy`. Homebrew's
current service command manages background services through macOS `launchctl`
or Linux `systemctl`; without `sudo` it operates on user LaunchAgents or user
systemd units and registers services to start at login.

This fits Grafana's macOS Alloy docs and keeps package upgrades inside
Homebrew, but it gives Graith the wrong ownership boundary. Grafana's current
macOS configuration docs say that changing the service itself requires editing
the Alloy formula and reinstalling it. Graith would need to mutate
Homebrew-managed files or rely on extra env/args files under the Homebrew
prefix, while `brew upgrade` could still replace service assumptions behind
Graith's receipt. On Linux, Homebrew services are much less common than native
systemd user units, and Grafana's Linux packages already install a system
service that Graith should not take over.

Homebrew remains a good way to install the Alloy binary on macOS. It should not
be the Graith-managed service layer.

### Proposal 3: Installed Alloy with Native User Services (Recommended)

This section describes the proposed end state. It is not implemented by this
design-only issue.

Graith manages a collector service only when the user asks it to. The service
runs an already installed Alloy binary, always passes `--disable-reporting`,
uses a Graith-generated config, and stores Alloy state under Graith's profile
data directory. Graith owns the native service definition, per-profile loopback
ports, secret projection files, and a receipt that records the desired state;
the package manager owns the binary.

The future user flow should be shaped like this, with final command names left
to the implementation design:

```text
gr observability collector init       # render config, validate, no start
gr observability collector enable     # install service and start it
gr observability collector status     # compare desired, rendered, and live state
gr observability collector reconcile  # repair generated files after validation
gr observability collector restart    # restart after package/user changes
gr observability collector rollback   # restore previous Graith-rendered config
gr observability collector remove     # stop/unregister, preserve data by default
```

Commands that create, reconcile, reload, or start export components fail closed
when telemetry export is not explicitly configured. Non-exporting recovery and
diagnostic commands such as `status`, `doctor`, and `remove` remain available so
Graith can explain disabled config, stop a previously managed service, or clean
up after drift. For example, a generated config may include a
`loki.source.file` block for daemon logs only when the user opted into log
collection, and session scrollback logs require a separate explicit opt-in
because they may contain prompts, source, command output, and secrets.

#### macOS scope

On macOS, Graith writes one per-profile LaunchAgent under
`~/Library/LaunchAgents`, with a label derived from a fixed Graith namespace and
the canonical profile. The plist uses absolute paths for the Alloy binary,
generated config, storage directory, stdout log, stderr log, and the
Graith-managed secret files referenced by config. It must not use a launchd
`EnvironmentVariables` dictionary for credentials, and it must not pretend that
launchd has a systemd-style `EnvironmentFile` directive. If an Alloy component
cannot consume a file-backed secret directly, Graith must use a Graith-owned
wrapper that reads an owner-only file and `exec`s Alloy rather than inlining
secret values in the plist.

`RunAtLoad` is true only after `enable`, so the collector starts at login for
that user. This deliberately differs from the accepted on-demand daemon
LaunchAgent design: the collector is a long-lived user service, while
`graithd` remains demand-started. `KeepAlive` is limited to abnormal exits or
non-successful termination; a deliberate `remove` or `stop` first unregisters or
disables the job with `launchctl bootout` or the equivalent current-user domain
operation before sending termination, so launchd does not undo an intentional
stop. `ThrottleInterval` bounds restart loops.

Graith should manage the job through exact launchd labels and current-user GUI
domains. It must never infer success from label prefix searches, never install
a LaunchDaemon, and never treat launchd metadata as the only source of truth.
The receipt, generated file digests, configured binary identity, and live
process command line all have to agree before `status` reports healthy.

This is deliberately not the old high-availability LaunchAgent model. There is
one user job per profile, no backup job, no secondary watcher, no hidden
auto-repair outside explicit Graith commands, and no claim that launchd alone
proves the right collector is running.

#### Linux scope

On Linux, Graith writes one per-profile unit under
`$XDG_CONFIG_HOME/systemd/user` or `~/.config/systemd/user`:

```ini
[Unit]
Description=Graith observability collector (profile-name)
StartLimitIntervalSec=5m
StartLimitBurst=5

[Service]
Type=simple
ExecStart=/absolute/path/to/alloy run --server.http.listen-addr=127.0.0.1:NNNN --disable-reporting --storage.path=/.../collector/storage /.../collector/config.alloy
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=default.target
```

The implementation should generate a concrete unit rather than rely on `%i`
escaping if that keeps profile and path validation simpler. Enablement uses
`systemctl --user daemon-reload` and `systemctl --user enable --now`; removal
uses `systemctl --user disable --now`, then removes only the Graith-owned unit
and generated files. Logs are read through `journalctl --user -u <unit>`.

Graith does not edit `/etc/alloy/config.alloy`, `/etc/default/alloy`,
`/etc/sysconfig/alloy`, `/var/lib/alloy`, or the package-owned system
`alloy.service`. Running as the current user lets the collector read the
current user's Graith files without broadening a package-managed `alloy` user.
If the user manager is unavailable or broken, Graith falls back to Proposal 1
behavior and reports that lifecycle management is unsupported.

Graith allocates or validates all loopback ports used by a managed profile: the
Alloy HTTP control endpoint and any generated OTLP, Prometheus, or other local
receivers. These ports are recorded in the receipt and rendered into config.
If the user configured `[telemetry.tracing] endpoint`, `enable` fails unless the
generated receiver address and Graith's exporter endpoint agree; Graith must not
silently start a collector that cannot receive the daemon's traces.

Graith also prefers stable package-managed binary entry points over
versioned package paths. For example, `/opt/homebrew/opt/alloy/bin/alloy` is a
better Homebrew target than a Cellar version path, and `/usr/bin/alloy` is the
expected Linux package path. The receipt records the configured path, resolved
real path where available, and `alloy --version` so package upgrades and broken
symlinks become explicit drift.

#### Operational contract

| Concern | Managed behavior |
|---------|------------------|
| Enable | Resolve and validate the Alloy binary, record `alloy --version`, require the CLI capability set used by this design (`run`, `validate`, `--disable-reporting`, `--storage.path`, and `--server.http.listen-addr`; verified during design against Alloy v1.18.0), allocate or validate per-profile loopback ports, render config/secret/service files to temporary paths, run `alloy validate` with the same feature/stability flags as `run`, atomically publish files, then start the native user service. Validation is a syntax and static-configuration gate, not proof that endpoints, permissions, or credentials will work at runtime. |
| Upgrade | Graith does not upgrade Alloy. After Homebrew, apt, dnf, zypper, tarball, or manual binary changes, `reconcile` validates the current binary and version against the generated config and required capability set, then restarts only after validation passes. |
| Rollback | Graith keeps the previous generated config, secret metadata, service manifest, port allocation, binary version, and receipt. A failed apply restores those files and restarts the prior service definition if the prior binary path still exists. Binary downgrade is a package-manager or user action. |
| Config drift | `status` compares file digests, service definition, configured binary path and version, allocated ports, live process args, and receipt. Drift is reported without overwriting by default; `reconcile` overwrites only Graith-owned generated files after validation. |
| Config reload | Config-only changes validate first, then Graith requests Alloy reload through the per-profile HTTP control endpoint recorded in the receipt. If reload fails, Alloy's documented behavior is to continue in the last valid state; Graith reports degraded and keeps the previous receipt. Flag, secret, storage, port, or binary changes require restart. |
| Crash recovery | Native service restart handles abnormal exits with backoff. Graith does not keep an in-process supervisor goroutine or spawn replacement collectors from `graithd`. Repeated restart failure becomes a platform service failure shown by `status` and doctor. On macOS, where launchd can keep retrying instead of entering a terminal failed state, Graith detects flapping from launchd state, process age, logs, and receipt timestamps. |
| Storage | Desired settings live in Graith config. Generated config, owner-only secret files, canonical rendered service manifests, receipts, stdout/stderr logs where applicable, and Alloy storage live under the profile's Graith data directory, with no credentials in plist/unit files. Installed native service definitions still live in the platform-required LaunchAgents or systemd user-unit directories and are checked against the canonical copy. |
| Uninstall | `remove` stops and unregisters only the Graith-owned user service, removes generated config/secret/service files and receipts, and preserves Alloy storage plus Graith telemetry/log state by default. A later `enable` reuses preserved storage only when receipt lineage proves it belongs to the same profile and collector kind; otherwise Graith reports orphaned storage and requires explicit reuse or purge. A separate explicit purge may remove Graith-owned collector storage, but never uninstalls Alloy itself. |

The main trade-off is that the user must install and upgrade Alloy separately.
That is acceptable because it keeps supply chain and rollback semantics honest:
Graith can promise service correctness for files it generated, not security
updates for an external collector binary.

### Proposal 4: Bundle Alloy in Graith

Graith could ship an Alloy binary inside its release artifacts and manage that
exact executable. This would make version selection and rollback easier for the
service manager, but it conflicts with the parent epic and creates an ongoing
distribution obligation. Graith would need to track Alloy releases, security
fixes, signing/notarization, platform support, binary size, and license notices,
then diagnose whether failures belong to Graith, Alloy, or the generated config.

Bundling also blurs the no-telemetry-by-default boundary. Alloy currently sends
anonymous usage statistics by default unless `--disable-reporting` is passed;
Graith can control that flag for a managed service, but bundling would still
make Alloy feel like part of Graith's own runtime rather than an explicit
external collector. The design therefore rejects bundling Alloy.

### Proposal 5: Direct Child Process Supervision

Graith could spawn `alloy run` as a child of `graithd` and restart it when it
exits. That avoids launchd and systemd files, but it couples collector lifetime
to daemon lifetime, complicates daemon restart/upgrade, and recreates service
supervision in Go. It also makes login/startup behavior different from the
platform service tools users already inspect. Native user services are a better
boundary for a long-lived collector.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/2047
- Parent epic: https://github.com/d0ugal/graith/issues/2037
- Existing user docs: `website/content/docs/configuration/observability.md`
- Telemetry runtime design:
  `docs/design/2026-07-31-telemetry-runtime.md`
- Initial metrics design:
  `docs/design/2026-07-31-initial-daemon-metrics.md`
- macOS daemon service design:
  `docs/design/2026-07-19-macos-daemon-app-identity.md`
- Grafana Alloy install/run/config docs:
  https://grafana.com/docs/alloy/latest/set-up/install/
- Grafana Alloy macOS install/run/config docs:
  https://grafana.com/docs/alloy/latest/set-up/install/macos/,
  https://grafana.com/docs/alloy/latest/set-up/run/macos/,
  https://grafana.com/docs/alloy/latest/configure/macos/
- Grafana Alloy Linux install/run/config docs:
  https://grafana.com/docs/alloy/latest/set-up/install/linux/,
  https://grafana.com/docs/alloy/latest/set-up/run/linux/,
  https://grafana.com/docs/alloy/latest/configure/linux/
- Grafana Alloy data collection and validation docs:
  https://grafana.com/docs/alloy/latest/data-collection/,
  https://grafana.com/docs/alloy/latest/reference/cli/validate/
- Homebrew `brew services` manpage:
  https://docs.brew.sh/Manpage#services-subcommand
- systemd unit, service, and systemctl manuals:
  https://www.freedesktop.org/software/systemd/man/systemd.unit.html,
  https://www.freedesktop.org/software/systemd/man/systemd.service.html,
  https://www.freedesktop.org/software/systemd/man/systemctl.html
- Apple launchd guidance:
  https://support.apple.com/guide/terminal/script-management-with-launchd-apdc6c1077b-5d5d-4d35-9c19-60f2397b2369/mac

### Implementation Notes

The first buildable slice after review should still be Proposal 1: generated
config plus validation and doctor checks. That slice proves path resolution and
config shape before any service file is installed.

The native-service slice should use the same transaction style as daemon
lifecycle work: reserve the desired state, render and validate candidates, act
on the platform service outside global locks, then commit or roll back the
receipt. A failed validation, missing binary, unreadable storage directory, or
service-manager error must leave the previous collector state untouched.

Config rendering should keep credentials out of service definitions and command
lines. Endpoint URLs and component topology may be generated, but backend tokens
should come from owner-only secret files or explicit environment-variable names
that the user configured outside the service file. Status and doctor output
must redact values and report only variable names, file paths, digests,
versions, allocated ports, and service state.

### Alternatives considered

OpenTelemetry Collector can be supported by the generated-config layer, but this
design chooses Alloy for managed lifecycle because the current Graith docs and
issue scope are Alloy-specific. A later design can add a collector-kind
abstraction if multiple managed collectors are worth the added surface.

A system-wide Linux service was rejected because it needs root operations,
different file permissions for user Graith logs, and coordination with the
package-owned `alloy.service`. A macOS LaunchDaemon was rejected for the same
reason: Graith's observability collection is per user and per profile.

### Testing

Design-only review should check that the proposed ownership boundary satisfies
#2047 before implementation starts.

Future implementation coverage should include pure unit tests for config
rendering, path/profile label derivation, digest comparison, redaction, and
state transitions; fake service-manager tests for enable, start failure,
restart failure, drift, rollback, reload, port collisions, orphaned storage, and
uninstall; and platform integration tests for macOS LaunchAgent and Linux
`systemd --user` behavior where CI supports them. Lifecycle and transaction
code should preserve the repository's Go coverage target and get race coverage
where it touches daemon state, locks, service callbacks, or file publication.
Tests must prove that default Graith config starts no collector, enables no
telemetry, and passes `--disable-reporting` whenever Graith starts Alloy.

### Open questions

- What exact CLI names should expose generated config versus managed lifecycle?
- Should Graith support user-provided Alloy fragments, or should all managed
  config be generated from structured Graith settings?
- How many rollback snapshots should Graith retain, and should storage purging
  share retention policy with existing Graith logs?
- Should collector status be added to the frontend capability matrix after the
  design is accepted, or remain CLI-only until a GUI use case appears?
