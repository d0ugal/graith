import Foundation
import ServiceManagement

private struct Response: Encodable {
    let operation: String
    let service: String
    let status: String
    let rawStatus: Int
}

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

private func fail(_ message: String, code: Int32 = 2) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(code)
}

private func isServiceManagementError(_ error: Error, _ code: Int) -> Bool {
    let nsError = error as NSError
    return nsError.domain == (kSMErrorDomainFramework as String) && nsError.code == code
}

private func isRegistrationApprovalError(_ error: Error) -> Bool {
    if isServiceManagementError(error, Int(kSMErrorLaunchDeniedByUser)) {
        return true
    }

    if #available(macOS 15.0, *) {
        let nsError = error as NSError
        // SMAppServiceErrorDomain code 1 is the approval-required result
        // returned by current Service Management implementations.
        return nsError.domain == (SMAppServiceErrorDomain as String) && nsError.code == 1
    }

    return false
}

private func writeResponse(operation: String, serviceName: String, status: SMAppService.Status) throws {
    let response = Response(
        operation: operation,
        service: serviceName,
        status: statusName(status),
        rawStatus: status.rawValue
    )
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    FileHandle.standardOutput.write(try encoder.encode(response))
    FileHandle.standardOutput.write(Data("\n".utf8))
}

@main
private enum GraithServiceController {
    static func main() {
        guard CommandLine.arguments.count == 3 else {
            fail("usage: graith-service-controller default|00..63 status|register|register-fresh|unregister")
        }

        let serviceName = CommandLine.arguments[1]
        let operation = CommandLine.arguments[2]
        guard let plistName = graithServicePlists[serviceName] else {
            fail("unknown service slot: \(serviceName)")
        }

        let service = SMAppService.agent(plistName: plistName)
        do {
            var normalizedStatus: SMAppService.Status?
            switch operation {
            case "register", "register-fresh":
                if operation == "register-fresh" && service.status != .notRegistered && service.status != .notFound {
                    fail("fresh registration requires an absent service, found \(statusName(service.status))")
                }

                do {
                    try service.register()
                } catch where isRegistrationApprovalError(error) || service.status == .requiresApproval {
                    normalizedStatus = .requiresApproval
                }
            case "unregister":
                do {
                    try service.unregister()
                } catch where isServiceManagementError(error, Int(kSMErrorJobNotFound)) || service.status == .notRegistered || service.status == .notFound {
                    normalizedStatus = .notRegistered
                }
            case "status":
                break
            default:
                fail("unknown operation: \(operation)")
            }

            try writeResponse(operation: operation, serviceName: serviceName, status: normalizedStatus ?? service.status)
        } catch {
            let nsError = error as NSError
            fail("\(nsError.domain):\(nsError.code): \(nsError.localizedDescription)", code: 1)
        }
    }
}
