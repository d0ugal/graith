// graith-notifier — a minimal macOS notification helper.
//
// `gr notify` used to post notifications via `osascript -e 'display
// notification …'`, which makes them appear under "Script Editor" in System
// Settings > Notifications — users could never find or configure graith's
// notification preferences (issue #1094). This helper instead posts via
// UNUserNotificationCenter from inside a proper .app bundle whose Info.plist
// carries CFBundleIdentifier = com.graith.notifier, so notifications show up
// under "Graith" and can be configured per-app like any other application.
//
// Usage: graith-notifier <title> <message> [priority]
//   priority "high" plays the default sound; anything else is silent.
//
// It exits 0 on success and non-zero on any failure (bad args, authorization
// denied, delivery error, timeout) so the Go caller can fall back to osascript.

import Foundation
import UserNotifications

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("graith-notifier: \(message)\n".utf8))
    exit(1)
}

let args = CommandLine.arguments
guard args.count >= 3 else {
    fail("usage: graith-notifier <title> <message> [priority]")
}

let title = args[1]
let body = args[2]
let priority = args.count >= 4 ? args[3] : "normal"

let center = UNUserNotificationCenter.current()
let done = DispatchSemaphore(value: 0)
var failure: String?

center.requestAuthorization(options: [.alert, .sound]) { granted, error in
    if let error = error {
        failure = "authorization error: \(error.localizedDescription)"
        done.signal()
        return
    }
    guard granted else {
        failure = "notification permission not granted"
        done.signal()
        return
    }

    let content = UNMutableNotificationContent()
    content.title = title
    content.body = body
    if priority == "high" {
        content.sound = .default
    }

    // trigger: nil delivers immediately. A per-request UUID identifier avoids
    // one notification replacing another when several arrive in quick succession.
    let request = UNNotificationRequest(
        identifier: UUID().uuidString, content: content, trigger: nil)
    center.add(request) { error in
        if let error = error {
            failure = "delivery error: \(error.localizedDescription)"
        }
        done.signal()
    }
}

// Bound the wait so a wedged authorization prompt can't hang the daemon caller
// (which already applies its own timeout, but be defensive).
if done.wait(timeout: .now() + 10) == .timedOut {
    fail("timed out waiting for notification delivery")
}

if let failure = failure {
    fail(failure)
}
