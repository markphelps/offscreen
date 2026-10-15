import Foundation
import Darwin

struct HostJob: Decodable, Sendable {
    let id: String
    let project: String
    let task: String
    let state: String
}

struct HostStatus: Decodable, Sendable {
    let queued: Int
    let running: HostJob?
    let waitingReason: String
    let countdownJob: String?
    let notNowJob: String?
    let countdownSeconds: Int?
    let returnJob: String?
    let graceSeconds: Int?
    let paused: Bool

    enum CodingKeys: String, CodingKey {
        case queued, running, paused
        case waitingReason = "waiting_reason"
        case countdownJob = "countdown_job"
        case notNowJob = "not_now_job"
        case countdownSeconds = "countdown_seconds"
        case returnJob = "return_job"
        case graceSeconds = "grace_seconds"
    }
}

/// A job as `GET /jobs` reports it; only the fields the app uses.
struct DaemonJob: Decodable, Sendable {
    let id: String
    let state: String

    /// Queued or in flight: cancelling it stops a worker or a queued entry.
    var active: Bool { !["done", "failed", "cancelled"].contains(state) }
}

/// The `GET /version` response. A daemon without the route is protocol 0.
struct DaemonVersion: Decodable, Sendable {
    let `protocol`: Int
    let version: String
}

struct Snapshot: Encodable, Sendable {
    let idleSeconds: Double
    let unlocked: Bool
    let displayAwake: Bool
    let cameraIdle: Bool
    let microphoneIdle: Bool
    let inputMonitoring: Bool
    let eventTapReady: Bool

    enum CodingKeys: String, CodingKey {
        case idleSeconds = "idle_seconds"
        case unlocked
        case displayAwake = "display_awake"
        case cameraIdle = "camera_idle"
        case microphoneIdle = "microphone_idle"
        case inputMonitoring = "input_monitoring"
        case eventTapReady = "event_tap_ready"
    }
}

enum BridgeError: Error {
    case socket(String)
    case invalidResponse
    case rejected(Int, String)
}

// Owner-only Unix socket; no HTTP server listening on a network interface.
struct Bridge: Sendable {
    let socketPath: String

    static func defaultSocket() -> String {
        let state = ProcessInfo.processInfo.environment["OFFSCREEN_STATE_DIR"]
            ?? (NSHomeDirectory() + "/Library/Application Support/offscreen")
        return state + "/offscreend.sock"
    }

    func request(_ path: String, snapshot: Snapshot? = nil) throws -> HostStatus {
        let body = try snapshot.map { try JSONEncoder().encode($0) } ?? Data()
        let payload = try send(path == "/host/status" ? "GET" : "POST", path, body: body)
        return try JSONDecoder().decode(HostStatus.self, from: payload)
    }

    func version() throws -> DaemonVersion {
        do {
            return try JSONDecoder().decode(DaemonVersion.self, from: send("GET", "/version"))
        } catch BridgeError.rejected(404, _) {
            return DaemonVersion(protocol: 0, version: "")
        }
    }

    func jobs() throws -> [DaemonJob] {
        try JSONDecoder().decode([DaemonJob].self, from: send("GET", "/jobs"))
    }

    /// Returns once the daemon has stopped the job and deleted its clone. The
    /// daemon bounds that at 130 seconds.
    func cancel(_ id: String) throws {
        _ = try send("POST", "/jobs/\(id)/cancel", timeout: 140)
    }

    private func send(_ method: String, _ path: String, body: Data = Data(), timeout: Int = 3) throws -> Data {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw BridgeError.socket("socket: \(errno)") }
        defer { _ = close(fd) }
        var noSigpipe: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSigpipe,
                         socklen_t(MemoryLayout<Int32>.size)) == 0 else {
            throw BridgeError.socket("configure socket: \(errno)")
        }
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let utf8 = Array(socketPath.utf8CString)
        let capacity = MemoryLayout.size(ofValue: address.sun_path)
        guard utf8.count <= capacity else { throw BridgeError.socket("socket path too long") }
        withUnsafeMutableBytes(of: &address.sun_path) { raw in
            for (index, byte) in utf8.enumerated() { raw[index] = UInt8(bitPattern: byte) }
        }
        let result = withUnsafePointer(to: &address) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard result == 0 else { throw BridgeError.socket("connect: \(errno)") }
        var receive = timeval(tv_sec: timeout, tv_usec: 0)
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &receive, socklen_t(MemoryLayout<timeval>.size))
        let header = "\(method) \(path) HTTP/1.0\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n"
        let bytes = Array(header.utf8) + body
        var sent = 0
        while sent < bytes.count {
            let count = bytes.withUnsafeBytes { raw in
                Darwin.write(fd, raw.baseAddress!.advanced(by: sent), bytes.count - sent)
            }
            guard count > 0 else { throw BridgeError.socket("write: \(errno)") }
            sent += count
        }
        var response = Data()
        var buffer = [UInt8](repeating: 0, count: 8192)
        while true {
            let count = Darwin.read(fd, &buffer, buffer.count)
            if count == 0 { break }
            guard count > 0, response.count < 65536 else { throw BridgeError.invalidResponse }
            response.append(contentsOf: buffer.prefix(count))
        }
        guard let separator = response.range(of: Data("\r\n\r\n".utf8)),
              let statusLine = String(data: response.prefix(100), encoding: .utf8)?.split(separator: "\r\n").first,
              let code = Int(statusLine.split(separator: " ").dropFirst().first ?? "") else {
            throw BridgeError.invalidResponse
        }
        let payload = response[separator.upperBound...]
        guard code == 200 else { throw BridgeError.rejected(code, String(decoding: payload, as: UTF8.self)) }
        return Data(payload)
    }
}
