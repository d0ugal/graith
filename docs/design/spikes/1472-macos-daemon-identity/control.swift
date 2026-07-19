import Foundation
import ServiceManagement

private let services = [
    "default": "net.graith.design-spike.daemon.plist",
    "slot-00": "net.graith.design-spike.daemon.profile.00.plist",
    "slot-01": "net.graith.design-spike.daemon.profile.01.plist",
]

private func statusName(_ status: SMAppService.Status) -> String {
    switch status {
    case .notRegistered:
        return "not-registered"
    case .enabled:
        return "enabled"
    case .requiresApproval:
        return "requires-approval"
    case .notFound:
        return "not-found"
    @unknown default:
        return "unknown-\(status.rawValue)"
    }
}

guard CommandLine.arguments.count == 3 else {
    FileHandle.standardError.write(
        Data("usage: graith-identity-spike-control default|slot-00|slot-01 register|status|unregister\n".utf8))
    exit(2)
}

guard let plistName = services[CommandLine.arguments[1]] else {
    FileHandle.standardError.write(Data("unknown service: \(CommandLine.arguments[1])\n".utf8))
    exit(2)
}

let service = SMAppService.agent(plistName: plistName)

do {
    switch CommandLine.arguments[2] {
    case "register":
        try service.register()
    case "unregister":
        try service.unregister()
    case "status":
        break
    default:
        FileHandle.standardError.write(Data("unknown operation: \(CommandLine.arguments[2])\n".utf8))
        exit(2)
    }

    print(statusName(service.status))
} catch {
    let nsError = error as NSError
    FileHandle.standardError.write(
        Data("\(nsError.domain):\(nsError.code): \(nsError.localizedDescription)\n".utf8))
    exit(1)
}
