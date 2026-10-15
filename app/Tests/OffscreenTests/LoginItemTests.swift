import Foundation
import XCTest
@testable import Offscreen

/// Stands in for SMAppService. Registering sets the status and, unless the
/// test says otherwise, gives the login job a pid.
final class FakeLogin: LoginService, @unchecked Sendable {
    private let lock = NSLock()
    private var current: LoginStatus
    private var log: [String] = []
    private var registerError: (any Error)?
    private var unregisterError: (any Error)?
    private var afterRegister: LoginStatus = .enabled
    private var jobAfterRegister: Int32? = 4242
    private var registers = 0
    private var jobFromRegister = 1
    private var registerDelay = 0.0

    init(status: LoginStatus = .notRegistered) { current = status }

    var calls: [String] { lock.withLock { log } }
    var status: LoginStatus { lock.withLock { current } }

    func failRegister(_ error: any Error) { lock.withLock { registerError = error } }
    func failUnregister(_ error: any Error) { lock.withLock { unregisterError = error } }
    func needApproval() { lock.withLock { afterRegister = .requiresApproval } }
    func neverStartJob() { lock.withLock { jobAfterRegister = nil } }
    /// The job runs only from the nth register call on, as macOS does with
    /// a newly installed build.
    func startJobFromRegister(_ count: Int) { lock.withLock { jobFromRegister = count } }
    /// Each register call blocks this long, as a slow BTM does.
    func slowRegister(_ seconds: Double) { lock.withLock { registerDelay = seconds } }

    func register() throws {
        let delay = lock.withLock { registerDelay }
        if delay > 0 { Thread.sleep(forTimeInterval: delay) }
        try lock.withLock {
            log.append("register")
            registers += 1
            if let registerError { throw registerError }
            current = afterRegister
        }
    }

    func unregister() throws {
        try lock.withLock {
            log.append("unregister")
            if let unregisterError { throw unregisterError }
            current = .notRegistered
        }
    }

    func jobPID() throws -> Int32? {
        lock.withLock {
            current == .enabled && log.last == "register" && registers >= jobFromRegister ? jobAfterRegister : nil
        }
    }

    func openSettings() { lock.withLock { log.append("settings") } }
}

private struct Failure: LocalizedError {
    var errorDescription: String? { "boom" }
}

final class LoginItemTests: XCTestCase {
    func testMenuGreysOutForASourceInstallAndOutsideABundle() {
        XCTAssertEqual(LoginItem.menu(status: .notRegistered, sourceInstall: true, bundled: true),
                       LoginItem.Menu(isOn: true, enabled: false, note: "Started at login by the source install"))
        XCTAssertEqual(LoginItem.menu(status: .enabled, sourceInstall: true, bundled: true).enabled, false,
                       "the source install's agent wins even if the app's own is registered")
        XCTAssertEqual(LoginItem.menu(status: .notFound, sourceInstall: false, bundled: false),
                       LoginItem.Menu(isOn: false, enabled: false, note: "Start at Login needs Offscreen.app"))
    }

    func testMenuFollowsTheRegistration() {
        XCTAssertEqual(LoginItem.menu(status: .notRegistered, sourceInstall: false, bundled: true),
                       LoginItem.Menu(isOn: false, enabled: true))
        XCTAssertEqual(LoginItem.menu(status: .notFound, sourceInstall: false, bundled: true),
                       LoginItem.Menu(isOn: false, enabled: true), "never registered reads notFound")
        XCTAssertEqual(LoginItem.menu(status: .enabled, sourceInstall: false, bundled: true),
                       LoginItem.Menu(isOn: true, enabled: true))
        XCTAssertEqual(LoginItem.menu(status: .requiresApproval, sourceInstall: false, bundled: true),
                       LoginItem.Menu(isOn: true, enabled: true, approve: true))
    }

    func testSourceInstallIsDetectedByItsAgentFile() throws {
        let home = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: home) }
        XCTAssertFalse(LoginItem.sourceInstalled(home: home))
        let agents = home.appending(path: "Library/LaunchAgents")
        try FileManager.default.createDirectory(at: agents, withIntermediateDirectories: true)
        try Data().write(to: agents.appending(path: "io.latentsignal.offscreend.plist"))
        XCTAssertFalse(LoginItem.sourceInstalled(home: home), "the daemon's agent isn't the app's")
        try Data().write(to: agents.appending(path: "io.latentsignal.offscreen.plist"))
        XCTAssertTrue(LoginItem.sourceInstalled(home: home))
    }

    func testOnlyTheLoginJobsCopyCountsAsStartedByTheAgent() {
        XCTAssertTrue(LoginItem.startedByAgent(environment: ["XPC_SERVICE_NAME": "io.latentsignal.offscreen"]))
        XCTAssertFalse(LoginItem.startedByAgent(environment: ["XPC_SERVICE_NAME": "io.latentsignal.offscreend"]))
        XCTAssertFalse(LoginItem.startedByAgent(environment: ["XPC_SERVICE_NAME": "0"]))
        XCTAssertFalse(LoginItem.startedByAgent(environment: [:]))
    }

    func testOnlyAHandOpenedCopyOfARegisteredBundleHandsOver() {
        func handOver(_ status: LoginStatus = .enabled, agent: Bool = false, source: Bool = false,
                      bundled: Bool = true) -> Bool {
            LoginItem.shouldHandOver(status: status, startedByAgent: agent, sourceInstall: source, bundled: bundled)
        }
        XCTAssertTrue(handOver())
        XCTAssertFalse(handOver(agent: true), "unregistering from the job's own copy would kill it")
        XCTAssertFalse(handOver(source: true))
        XCTAssertFalse(handOver(bundled: false))
        for status in [LoginStatus.notRegistered, .notFound, .requiresApproval] {
            XCTAssertFalse(handOver(status))
        }
    }

    func testHandOverRegistersAgainAndWaitsForTheJob() {
        let login = FakeLogin(status: .enabled)
        XCTAssertEqual(LoginItem.handOver(login, reregister: true, self: 1, within: 1), .running)
        XCTAssertEqual(login.calls, ["unregister", "register"],
                       "an upgraded bundle needs unregister then register")
    }

    func testHandOverReportsApprovalFailureAndAJobThatNeverStarts() {
        let approval = FakeLogin()
        approval.needApproval()
        XCTAssertEqual(LoginItem.handOver(approval, reregister: false, self: 1, within: 1), .needsApproval)

        let failing = FakeLogin()
        failing.failRegister(Failure())
        XCTAssertEqual(LoginItem.handOver(failing, reregister: false, self: 1, within: 1),
                       .failed("Start at Login failed: boom"))

        let stuck = FakeLogin()
        stuck.neverStartJob()
        guard case .failed = LoginItem.handOver(stuck, reregister: false, self: 1, within: 0.2) else {
            return XCTFail("a job that never starts must not let the copy exit")
        }
    }

    func testHandOverIgnoresAJobPIDThatIsThisCopy() {
        let login = FakeLogin()
        guard case .failed = LoginItem.handOver(login, reregister: false, self: 4242, within: 0.2) else {
            return XCTFail("only another pid is the login job's copy")
        }
    }

    func testOnlyAnEnabledAgentInABundleIsRenewed() {
        XCTAssertTrue(LoginItem.shouldRenew(status: .enabled, sourceInstall: false, bundled: true))
        for status in [LoginStatus.notRegistered, .notFound, .requiresApproval] {
            XCTAssertFalse(LoginItem.shouldRenew(status: status, sourceInstall: false, bundled: true),
                           "an upgrade must not turn Start at Login on (\(status))")
        }
        XCTAssertFalse(LoginItem.shouldRenew(status: .enabled, sourceInstall: true, bundled: true))
        XCTAssertFalse(LoginItem.shouldRenew(status: .enabled, sourceInstall: false, bundled: false))
    }

    func testRenewAfterTheRecordWasPurgedRegistersAgain() {
        // Homebrew's purge removed the record, so the status no longer reads enabled.
        let login = FakeLogin(status: .notFound)
        XCTAssertEqual(LoginItem.renew(login, self: 1, within: 1, retry: 0), .running)
        XCTAssertEqual(login.calls, ["register", "unregister", "register"])
    }

    func testRenewRefusedForANewBuildIsRetriedOnce() {
        let login = FakeLogin(status: .enabled)
        login.startJobFromRegister(2) // macOS refuses the first registration of a new build
        XCTAssertEqual(LoginItem.renew(login, self: 1, within: 1, retry: 0), .running)
        XCTAssertEqual(login.calls, ["unregister", "register", "unregister", "register"])

        let stuck = FakeLogin(status: .enabled)
        stuck.neverStartJob()
        guard case .failed = LoginItem.renew(stuck, self: 1, within: 0.2, retry: 0) else {
            return XCTFail("a job that never starts fails after one retry")
        }
        XCTAssertEqual(stuck.calls.filter { $0 == "register" }.count, 2, "only one retry")
    }

    func testRenewDoesNotRetryAnApproval() {
        let login = FakeLogin(status: .enabled)
        login.needApproval()
        XCTAssertEqual(LoginItem.renew(login, self: 1, within: 1, retry: 0), .needsApproval)
        XCTAssertEqual(login.calls, ["unregister", "register"])
    }

    func testWaitForExitReturnsOnceTheOthersAreGone() {
        var checks = 0
        XCTAssertTrue(SingleInstance.waitForExit([10], within: 5, alive: { _ in checks += 1; return checks < 3 }))
        XCTAssertTrue(SingleInstance.waitForExit([], within: 0, alive: { _ in true }))
        XCTAssertFalse(SingleInstance.waitForExit([10], within: 0.2, alive: { _ in true }))
    }
}

@MainActor
final class StartAtLoginTests: XCTestCase {
    private final class Exits { var count = 0 }

    private func model(_ login: FakeLogin, agent: Bool, control: DaemonLauncher = NoDaemon(),
                       confirm: @escaping @MainActor (Confirmation) -> Bool = { _ in true },
                       exits: Exits) -> HostModel {
        HostModel(bridge: Bridge(socketPath: "/nonexistent/offscreen.sock"), manualReturn: false, verifiedFilter: true,
                  post: { _ in }, control: control, confirm: confirm, login: login, startedByAgent: agent,
                  sourceInstalled: { false }, bundled: true, exitApp: { exits.count += 1 },
                  handOverRetry: 0.05)
    }

    func testAQuitDuringAHandoverKeepsThisCopyRunning() async {
        let login = FakeLogin()
        login.slowRegister(0.3)
        let exits = Exits()
        let model = model(login, agent: false, exits: exits)
        let tick = model.setStartAtLogin(true)
        while !model.handingOver { await Task.yield() }
        XCTAssertNil(model.setStartAtLogin(true), "a second tick waits for the handover")
        XCTAssertNil(model.restart(), "Restart waits for the handover")
        _ = await model.shutDown(verb: "Quit")
        await tick?.value
        XCTAssertEqual(exits.count, 0, "the quit wins; this copy must not exit mid-quit")
        XCTAssertEqual(login.calls.filter { $0 == "register" }.count, 2, "one handover only")
    }

    func testTurningStartAtLoginOffCancelsAPendingRetry() async {
        let login = FakeLogin()
        login.neverStartJob()
        let exits = Exits()
        let model = model(login, agent: false, exits: exits)
        let retry = model.retryHandOver(after: "failed")
        _ = await model.setStartAtLogin(false)?.value
        await retry.value
        XCTAssertEqual(login.calls, ["unregister"], "the cancelled retry never registers")
        XCTAssertNil(model.actionError)
        XCTAssertEqual(exits.count, 0)
    }

    func testAHandoverRefusedForANewBuildIsRetriedOnce() async {
        let login = FakeLogin()
        login.startJobFromRegister(3) // the first registration cycle doesn't start the job
        let exits = Exits()
        let model = model(login, agent: false, exits: exits)
        await model.setStartAtLogin(true)?.value
        XCTAssertEqual(login.calls, ["register", "unregister", "register", "unregister", "register"])
        XCTAssertEqual(exits.count, 1, "the retry hands over")
        XCTAssertNil(model.actionError, "no error while the retry succeeds")
    }

    func testTickingFromAHandOpenedCopyHandsOverAndExits() async {
        let login = FakeLogin()
        let exits = Exits()
        let model = model(login, agent: false, exits: exits)
        await model.setStartAtLogin(true)?.value
        XCTAssertEqual(login.calls, ["register", "unregister", "register"], "a stale record from an earlier build must not block the job")
        XCTAssertEqual(exits.count, 1, "the login job's copy takes over")
        XCTAssertEqual(model.loginItem, LoginItem.Menu(isOn: true, enabled: true))
    }

    func testTickingThatNeedsApprovalStaysAndOffersSettings() async {
        let login = FakeLogin()
        login.needApproval()
        let exits = Exits()
        let model = model(login, agent: false, exits: exits)
        await model.setStartAtLogin(true)?.value
        XCTAssertEqual(exits.count, 0)
        XCTAssertTrue(model.loginItem.approve)
        model.approveLoginItem()
        XCTAssertEqual(login.calls.last, "settings")
    }

    func testUntickingFromAHandOpenedCopyOnlyUnregisters() async {
        let login = FakeLogin(status: .enabled)
        let exits = Exits()
        var asked = 0
        let model = model(login, agent: false, confirm: { _ in asked += 1; return true }, exits: exits)
        await model.setStartAtLogin(false)?.value
        XCTAssertEqual(login.calls, ["unregister"])
        XCTAssertEqual(exits.count, 0)
        XCTAssertEqual(asked, 0)
        XCTAssertFalse(model.loginItem.isOn)
    }

    func testUntickingFromTheJobsCopyStopsTheDaemonBeforeUnregistering() async {
        let login = FakeLogin(status: .enabled)
        let control = RecordingDaemon(login: login)
        let exits = Exits()
        let model = model(login, agent: true, control: control, exits: exits)
        await model.setStartAtLogin(false)?.value
        XCTAssertEqual(control.order, ["stop", "unregister"], "unregistering kills this copy without asking it")
        XCTAssertEqual(exits.count, 1)
    }

    func testDecliningToTurnOffLeavesEverythingRunning() async {
        let login = FakeLogin(status: .enabled)
        let control = RecordingDaemon(login: login)
        let exits = Exits()
        let model = model(login, agent: true, control: control, confirm: { _ in false }, exits: exits)
        await model.setStartAtLogin(false)?.value
        XCTAssertEqual(control.order, [])
        XCTAssertEqual(exits.count, 0)
        XCTAssertFalse(model.controlling)
    }

    func testAFailedUnregisterStartsTheDaemonAgain() async {
        let login = FakeLogin(status: .enabled)
        login.failUnregister(Failure())
        let control = RecordingDaemon(login: login)
        let exits = Exits()
        let model = model(login, agent: true, control: control, exits: exits)
        await model.setStartAtLogin(false)?.value
        XCTAssertEqual(exits.count, 0)
        XCTAssertEqual(model.actionError, "Turning off Start at Login failed: boom")
        XCTAssertFalse(model.controlling)
        while !control.order.contains("start") { await Task.yield() }
        XCTAssertEqual(control.order, ["stop", "unregister", "start"])
    }

    func testADaemonThatDidNotStopKeepsStartAtLogin() async {
        let login = FakeLogin(status: .enabled)
        let control = RecordingDaemon(login: login, failStop: true)
        let exits = Exits()
        let model = model(login, agent: true, control: control, exits: exits)
        await model.setStartAtLogin(false)?.value
        XCTAssertEqual(control.order, ["stop"])
        XCTAssertEqual(exits.count, 0)
        XCTAssertFalse(model.controlling)
        XCTAssertEqual(model.actionError,
                       "Start at Login is still on. The daemon didn't stop: The daemon didn't stop within 20 seconds.")
    }
}

private struct NoDaemon: DaemonLauncher {
    func start() throws {}
    func stop(within seconds: Double) throws {}
}

/// Records daemon starts and stops in order with the login service's
/// unregister calls.
private final class RecordingDaemon: DaemonLauncher, @unchecked Sendable {
    private let lock = NSLock()
    private var log: [String] = []
    private let login: FakeLogin
    private let failStop: Bool

    init(login: FakeLogin, failStop: Bool = false) {
        self.login = login
        self.failStop = failStop
    }

    var order: [String] {
        let unregistered = login.calls.contains("unregister")
        return lock.withLock {
            guard unregistered, let stop = log.firstIndex(of: "stop") else { return log }
            var merged = log
            merged.insert("unregister", at: stop + 1)
            return merged
        }
    }

    func start() throws { lock.withLock { log.append("start") } }

    func stop(within seconds: Double) throws {
        lock.withLock { log.append("stop") }
        if failStop { throw DaemonControlError.stillRunning(seconds: 20) }
    }
}
