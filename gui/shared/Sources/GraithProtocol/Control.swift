import Foundation

/// The graith control-protocol version. Compatibility is by major version,
/// matching `protocol.Version` / `VersionCompatible` in Go.
public let protocolVersion = "1.0"

/// Reports whether a peer's version string is compatible with ours (same
/// major version), mirroring `protocol.VersionCompatible`.
public func versionCompatible(_ v: String) -> Bool {
    guard let ourMajor = protocolVersion.split(separator: ".").first,
          let theirMajor = v.split(separator: ".").first else {
        return false
    }
    return ourMajor == theirMajor
}

/// A control-channel envelope: `{type, payload, token}`.
///
/// Mirrors `protocol.Envelope`. The `payload` is left as raw JSON so callers
/// decode it into the concrete message type keyed off `type`. `token`, when
/// present, is the caller's bearer credential (session token locally, paired
/// client token remotely) — it is included in the JSON exactly as the Go
/// envelope does (`token,omitempty`).
public struct ControlEnvelope: Sendable {
    public let type: String
    public let payload: Data?
    public let token: String?

    public init(type: String, payload: Data? = nil, token: String? = nil) {
        self.type = type
        self.payload = payload
        self.token = token
    }
}

public enum ControlError: Error {
    case malformed(String)
    /// The daemon replied with an `error` control message.
    case daemon(String)
    /// The handshake was rejected (version/profile mismatch, etc.).
    case handshakeRejected(String)
    /// A reply of an unexpected type arrived.
    case unexpectedReply(String)
    /// During pairing, the SPKI pin the daemon reported did not match the pin of
    /// the certificate actually presented on the TLS handshake (possible MITM),
    /// or the daemon reported no pin at all.
    case tlsPinMismatch(String)
}

private let jsonEncoder: JSONEncoder = {
    let e = JSONEncoder()
    // Match Go's compact marshalling; key order is irrelevant to the daemon.
    return e
}()

private let jsonDecoder = JSONDecoder()

/// Encode a control message to envelope JSON, optionally carrying a token.
///
/// Equivalent to `protocol.EncodeControl` /
/// `protocol.EncodeControlWithToken`: the payload is marshalled, then wrapped
/// in `{"type":…,"payload":…,"token":…}`. `token` and an empty payload are
/// omitted, matching the Go `omitempty` tags so byte-compatible envelopes are
/// produced.
public func encodeControl<T: Encodable>(
    _ type: String,
    _ payload: T,
    token: String? = nil
) throws -> Data {
    let payloadData = try jsonEncoder.encode(payload)
    return try assembleEnvelope(type: type, payload: payloadData, token: token)
}

/// Encode a control message that has no payload (e.g. `list`, `pair_list`).
public func encodeControl(_ type: String, token: String? = nil) throws -> Data {
    try assembleEnvelope(type: type, payload: nil, token: token)
}

private func assembleEnvelope(type: String, payload: Data?, token: String?) throws -> Data {
    // Build the JSON object by hand so the embedded payload stays raw (a
    // `json.RawMessage` in Go) rather than being re-encoded as a string.
    var obj = Data()
    obj.append(Data("{\"type\":".utf8))
    obj.append(try jsonEncoder.encode(type))
    if let payload, !payload.isEmpty {
        obj.append(Data(",\"payload\":".utf8))
        obj.append(payload)
    }
    if let token, !token.isEmpty {
        obj.append(Data(",\"token\":".utf8))
        obj.append(try jsonEncoder.encode(token))
    }
    obj.append(Data("}".utf8))
    return obj
}

/// Decode a control envelope's `type` and raw `payload` from frame bytes,
/// mirroring `protocol.DecodeControl`.
///
/// The `payload` is kept as raw JSON bytes (re-serialized from the parsed
/// object) so it can later be decoded into whichever concrete message type
/// the `type` field selects — the Swift analogue of Go's `json.RawMessage`.
public func decodeControl(_ raw: Data) throws -> ControlEnvelope {
    guard let obj = try? JSONSerialization.jsonObject(with: raw) as? [String: Any] else {
        throw ControlError.malformed("decode control: not a JSON object")
    }
    guard let type = obj["type"] as? String else {
        throw ControlError.malformed("decode control: missing type")
    }

    var payloadData: Data?
    if let payload = obj["payload"] {
        // Re-serialize the payload sub-value verbatim so downstream decoding
        // works regardless of its shape (object, array, scalar).
        payloadData = try? JSONSerialization.data(
            withJSONObject: payload,
            options: [.fragmentsAllowed]
        )
    }

    let token = obj["token"] as? String
    return ControlEnvelope(type: type, payload: payloadData, token: token)
}

/// Decode an envelope's payload into a concrete message, mirroring
/// `protocol.DecodePayload`.
public func decodePayload<T: Decodable>(_ envelope: ControlEnvelope, as _: T.Type) throws -> T {
    guard let payload = envelope.payload, !payload.isEmpty,
          String(decoding: payload, as: UTF8.self) != "null" else {
        throw ControlError.malformed("decode payload: missing or null payload")
    }
    return try jsonDecoder.decode(T.self, from: payload)
}
