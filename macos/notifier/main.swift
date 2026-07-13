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
// Exit codes (read by the Go caller in internal/daemon/pushnotify.go):
//   0  success
//   2  usage error
//   3  the user has explicitly disabled notifications for Graith — the caller
//      MUST honour this and must NOT fall back to osascript (which would route
//      the notification around the user's opt-out via "Script Editor")
//   1  any other failure (permission not yet determined, delivery error,
//      timeout) — the caller may fall back to osascript

import Foundation
import UserNotifications

// exitDenied signals an explicit user opt-out; see the exit-code table above.
let exitDenied: Int32 = 3

func fail(_ message: String, code: Int32 = 1) -> Never {
    FileHandle.standardError.write(Data("graith-notifier: \(message)\n".utf8))
    exit(code)
}

let args = CommandLine.arguments
guard args.count >= 3 else {
    fail("usage: graith-notifier <title> <message> [priority]", code: 2)
}

let title = args[1]
let body = args[2]
let priority = args.count >= 4 ? args[3] : "normal"

let center = UNUserNotificationCenter.current()
let done = DispatchSemaphore(value: 0)
var failure: (message: String, code: Int32)?

center.requestAuthorization(options: [.alert, .sound]) { granted, error in
    if let error = error {
        failure = ("authorization error: \(error.localizedDescription)", 1)
        done.signal()
        return
    }
    guard granted else {
        // Distinguish an explicit user opt-out (.denied) from a merely
        // not-yet-determined state so the Go caller can honour the former
        // instead of falling back to osascript.
        center.getNotificationSettings { settings in
            if settings.authorizationStatus == .denied {
                failure = ("notifications are disabled for Graith", exitDenied)
            } else {
                failure = ("notification permission not granted", 1)
            }
            done.signal()
        }
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
            failure = ("delivery error: \(error.localizedDescription)", 1)
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
    fail(failure.message, code: failure.code)
}
