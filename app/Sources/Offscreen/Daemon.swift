import Foundation

enum DaemonControlError: Error, CustomStringConvertible {
    case launchctl(String)
    case notInstalled
    case stillRunning

    var description: String {
        switch self {
        case .launchctl(let message): message
        case .notInstalled: "The daemon isn't installed. Run mise run install in the offscreen checkout."
        case .stillRunning: "The daemon didn't stop within 20 seconds."
        }
    }
}

/// Starts and stops offscreend through its LaunchAgent. The agent has no
/// RunAtLoad or KeepAlive, so it runs only between the app's start and stop,
/// under its own OffscreenDaemon.app identity (and Local Network grant).
struct DaemonControl: Sendable {
    static let label = "io.latentsignal.offscreend"
    var target: String { "gui/\(getuid())/\(Self.label)" }

    /// Starts the daemon. Does nothing if it is already running.
    func start() throws {
        _ = try launchctl("kickstart", target)
    }

    /// Asks the daemon to exit with SIGTERM, its normal shutdown, and waits
    /// until launchd reports no process.
    func stop() throws {
        guard try pid() != nil else { return }
        _ = try launchctl("kill", "SIGTERM", target)
        for _ in 0..<200 {
            if try pid() == nil { return }
            Thread.sleep(forTimeInterval: 0.1)
        }
        throw DaemonControlError.stillRunning
    }

    func pid() throws -> Int32? {
        Self.pid(fromPrint: try launchctl("print", target))
    }

    /// Reads the service's top-level `pid = N` line. A stopped service has none.
    static func pid(fromPrint output: String) -> Int32? {
        for line in output.split(separator: "\n") where line.hasPrefix("\tpid = ") {
            return Int32(line.dropFirst("\tpid = ".count).trimmingCharacters(in: .whitespaces))
        }
        return nil
    }

    private func launchctl(_ arguments: String...) throws -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = arguments
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let output = String(decoding: data, as: UTF8.self)
        guard process.terminationStatus == 0 else {
            if output.contains("Could not find service") { throw DaemonControlError.notInstalled }
            let message = output.trimmingCharacters(in: .whitespacesAndNewlines)
            throw DaemonControlError.launchctl("launchctl \(arguments.joined(separator: " ")): \(message)")
        }
        return output
    }
}

/// What the menu says about the daemon.
enum DaemonState: Equatable {
    case starting
    case restarting
    case stopping
    case running(version: String)
    /// The daemon's build differs from this install's, usually because it
    /// was rebuilt without restarting the daemon.
    case mismatch(daemon: String, app: String)
    case notResponding(String)

    /// Compares builds only when both are stamped. Plain `go build` and
    /// `swift build` report `dev` and match anything.
    static func reachable(daemon: String, app: String?) -> DaemonState {
        let shown = daemon.isEmpty ? "protocol 0" : daemon
        guard let app, !app.isEmpty, app != "dev", !daemon.isEmpty, daemon != "dev", daemon != app else {
            return .running(version: shown)
        }
        return .mismatch(daemon: daemon, app: app)
    }

    var busy: Bool { [.starting, .restarting, .stopping].contains(self) }

    var summary: String {
        switch self {
        case .starting: "Starting daemon…"
        case .restarting: "Restarting daemon…"
        case .stopping: "Stopping daemon…"
        case .running(let version): "Daemon running · \(version)"
        case .mismatch(let daemon, let app): "Daemon \(daemon) doesn't match Offscreen \(app). Restart it."
        case .notResponding: "Daemon not responding"
        }
    }
}

enum QuitPrompt {
    /// "Quit and cancel 2 jobs?"
    static func message(verb: String, count: Int) -> String {
        "\(verb) and cancel \(count) \(count == 1 ? "job" : "jobs")?"
    }

    /// macOS sets a quit reason on the quit event for logout, restart and
    /// shutdown. Those quits cancel without asking, since the session is ending.
    static func systemQuit(reason: OSType?) -> Bool { reason != nil }
}
