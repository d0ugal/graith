# Issue 1472 macOS daemon-identity spike

This disposable spike tests whether separately labelled, app-associated
per-user LaunchAgents registered with `SMAppService` run the real
`gr daemon start` process under one Graith owner independently of the invoking
terminal. It is not the production service implementation.

On macOS 13 or later:

```sh
app=$(./docs/design/spikes/1472-macos-daemon-identity/build.sh)
control="$app/Contents/MacOS/graith-identity-spike-control"

for service in default slot-00 slot-01; do
    "$control" "$service" register
done

for label in \
    net.graith.design-spike.daemon \
    net.graith.design-spike.daemon.profile.00 \
    net.graith.design-spike.daemon.profile.01
do
    launchctl kickstart -kp "gui/$(id -u)/$label"
    launchctl print "gui/$(id -u)/$label"
done
```

The embedded plist intentionally has neither `RunAtLoad` nor `KeepAlive`.
Registration loads each job; `kickstart` represents a first CLI command asking
for the daemon. The three jobs use isolated spike profiles. The numbered names
model a finite pool of signed profile slots without implementing allocation.

To reproduce independent removal, re-registration, and no-restart behavior:

```sh
"$control" slot-00 unregister
for service in default slot-00 slot-01; do
    "$control" "$service" status
done

"$control" slot-00 register
launchctl kickstart -kp \
    "gui/$(id -u)/net.graith.design-spike.daemon.profile.00"

launchctl kill SIGTERM \
    "gui/$(id -u)/net.graith.design-spike.daemon.profile.01"
sleep 2
launchctl print \
    "gui/$(id -u)/net.graith.design-spike.daemon.profile.01"
```

The cross-copy check builds two same-identifier app generations at different
paths, registers with the first controller, and queries/unregisters with the
second:

```sh
./docs/design/spikes/1472-macos-daemon-identity/cross-copy.sh
```

Always remove the test registration before deleting the temporary app:

```sh
for service in slot-01 slot-00 default; do
    "$control" "$service" unregister
done
```

The build and cross-copy scripts print and retain their temporary app paths for
inspection. The real daemon also creates profile-specific config/data/runtime
directories for `identity-spike`, `identity-spike-slot-a`, and
`identity-spike-slot-b`; inspect and remove those separately after confirming
they contain no data you intend to keep. Release version parity, the protected
startup-request bootstrap, and Developer ID/notarization are out of scope for
this ad-hoc spike.

The design record contains the captured command-line evidence and identifies
the manual Activity Monitor result that production acceptance must still record.
