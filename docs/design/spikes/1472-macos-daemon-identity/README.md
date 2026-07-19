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

Always remove the test registration before deleting the temporary app:

```sh
for service in slot-01 slot-00 default; do
    "$control" "$service" unregister
done
```

The design record contains the captured command-line evidence and identifies
the manual Activity Monitor result that production acceptance must still record.
