import Foundation
import os
import ServiceManagement

/// SMAppService's status, as the menu needs it.
enum LoginStatus: Equatable {
    case notRegistered
    case enabled
    case requiresApproval
    /// Not registered yet, or the bundle has no embedded agent plist.
    case notFound
}

/// Start at Login through the agent plist sealed into the bundle
/// (app/scripts/bundle.sh); tests substitute their own.
protocol LoginService: Sendable {
    var status: LoginStatus { get }
    func register() throws
    func unregister() throws
    /// The pid of the running login job, if launchd has one.
    func jobPID() throws -> Int32?
    func openSettings()
}

struct AgentLoginService: LoginService {
    static let label = "io.latentsignal.offscreen"
    private var service: SMAppService { .agent(plistName: "\(Self.label).plist") }

    var status: LoginStatus {
        switch service.status {
        case .enabled: .enabled
        case .requiresApproval: .requiresApproval
        case .notRegistered: .notRegistered
        default: .notFound
        }
    }

    func register() throws { try service.register() }
    func unregister() throws { try service.unregister() }

    func jobPID() throws -> Int32? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = ["print", "gui/\(getuid())/\(Self.label)"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = FileHandle.nullDevice
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { return nil }
        return DaemonControl.pid(fromPrint: String(decoding: data, as: UTF8.self))
    }

    func openSettings() { SMAppService.openSystemSettingsLoginItems() }
}

/// Where this copy of the app came from, for Start at Login.
enum LoginItem {
    /// launchd sets XPC_SERVICE_NAME to the job's label for the copy it
    /// starts, from the app's own agent or the source install's.
    static func startedByAgent(environment: [String: String] = ProcessInfo.processInfo.environment) -> Bool {
        environment["XPC_SERVICE_NAME"] == AgentLoginService.label
    }

    /// scripts/install.sh writes this agent. It shares the label with the
    /// app's own agent, so only the file tells them apart.
    static func sourceInstalled(home: URL = FileManager.default.homeDirectoryForCurrentUser) -> Bool {
        FileManager.default.fileExists(atPath: home.appending(
            path: "Library/LaunchAgents/\(AgentLoginService.label).plist").path)
    }

    /// Whether the bundle carries the agent plist; `swift run` has no bundle.
    static func bundled(_ bundle: URL = Bundle.main.bundleURL) -> Bool {
        FileManager.default.fileExists(atPath: bundle.appending(
            path: "Contents/Library/LaunchAgents/\(AgentLoginService.label).plist").path)
    }

    /// What the Start at Login checkbox shows.
    struct Menu: Equatable {
        var isOn: Bool
        var enabled: Bool
        var note: String?
        /// Show "Approve Start at Login…".
        var approve = false
    }

    static func menu(status: LoginStatus, sourceInstall: Bool, bundled: Bool) -> Menu {
        if sourceInstall { return Menu(isOn: true, enabled: false, note: "Started at login by the source install") }
        guard bundled else { return Menu(isOn: false, enabled: false, note: "Start at Login needs Offscreen.app") }
        switch status {
        case .enabled: return Menu(isOn: true, enabled: true)
        case .requiresApproval: return Menu(isOn: true, enabled: true, approve: true)
        case .notRegistered, .notFound: return Menu(isOn: false, enabled: true)
        }
    }

    /// Whether a copy that isn't the login job should hand over to it: it is
    /// registered, and macOS keeps an upgraded bundle's registration but
    /// refuses to start it until it is registered again.
    static func shouldHandOver(status: LoginStatus, startedByAgent: Bool, sourceInstall: Bool, bundled: Bool) -> Bool {
        status == .enabled && !startedByAgent && !sourceInstall && bundled
    }

    private static let log = Logger(subsystem: "io.latentsignal.offscreen", category: "login")

    enum HandOver: Equatable {
        /// launchd runs the login job; this copy should exit 0.
        case running
        /// Registered, but the user must allow it in System Settings.
        case needsApproval
        case failed(String)
    }

    /// Registers so launchd starts the login job from this bundle, and waits
    /// for its pid. Unregistering first matters: after an upgrade a plain
    /// register() leaves launchd refusing the new signature. On .running the
    /// caller exits 0 without stopping the daemon; the job's copy waits for
    /// that exit (SingleInstance.waitForExit) and keeps the daemon.
    static func handOver(_ service: any LoginService, reregister: Bool, self pid: Int32 = getpid(),
                         within seconds: Double = 5) -> HandOver {
        do {
            if reregister { try service.unregister() }
            try service.register()
        } catch {
            log.warning("start at login: register failed: \(error, privacy: .public)")
            return .failed("Start at Login failed: \(error.localizedDescription)")
        }
        if service.status == .requiresApproval { return .needsApproval }
        let deadline = Date().addingTimeInterval(seconds)
        while true {
            if let job = try? service.jobPID(), job != pid { return .running }
            if Date() >= deadline { break }
            Thread.sleep(forTimeInterval: 0.1)
        }
        log.warning("start at login: launchd didn't start the login job within \(seconds)s")
        return .failed("Start at Login is on, but launchd didn't start Offscreen. It may start at the next login.")
    }
}
