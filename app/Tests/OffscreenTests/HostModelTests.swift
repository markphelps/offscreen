import Dispatch
import Foundation
import XCTest
@testable import Offscreen

/// Answers HostModel's daemon calls from canned replies. A gated path blocks
/// until the test signals it, so a test can choose which reply lands first.
private final class FakeBridge: HostBridge, @unchecked Sendable {
    private let lock = NSLock()
    private var replies: [String: HostStatus] = [:]
    private var conflicts: Set<String> = []
    private var gates: [String: DispatchSemaphore] = [:]
    private var jobList: [DaemonJob] = []
    private var down = false
    private var log: [String] = []

    var calls: [String] { lock.withLock { log } }

    func reply(_ path: String, _ status: HostStatus) { lock.withLock { replies[path] = status } }
    func conflict(_ path: String) { lock.withLock { _ = conflicts.insert(path) } }
    func setJobs(_ jobs: [DaemonJob]) { lock.withLock { jobList = jobs } }
    func setDown(_ value: Bool) { lock.withLock { down = value } }

    func gate(_ path: String) -> DispatchSemaphore {
        let gate = DispatchSemaphore(value: 0)
        lock.withLock { gates[path] = gate }
        return gate
    }

    func request(_ path: String, snapshot: Snapshot?) throws -> HostStatus {
        let (gate, reply, conflict, down) = lock.withLock {
            log.append(path)
            return (gates[path], replies[path], conflicts.contains(path), self.down)
        }
        gate?.wait()
        if down { throw BridgeError.socket("connect: 61") }
        if conflict { throw BridgeError.rejected(409, "host job is not active") }
        guard let reply else { throw BridgeError.rejected(404, path) }
        return reply
    }

    func version() throws -> DaemonVersion { DaemonVersion(protocol: 1, version: "test") }

    func jobs() throws -> [DaemonJob] {
        try lock.withLock {
            if down { throw BridgeError.socket("connect: 61") }
            return jobList
        }
    }

    func cancel(_ id: String) throws { lock.withLock { log.append("cancel \(id)") } }
}

/// Starts and stops a pretend daemon. A gated stop blocks until the test
/// signals it, then fails or succeeds as set.
private final class FakeControl: DaemonLauncher, @unchecked Sendable {
    private let lock = NSLock()
    private var log: [String] = []
    private var stopError: (any Error)?
    private var stopGate: DispatchSemaphore?

    var calls: [String] { lock.withLock { log } }

    func failStop(_ error: any Error) { lock.withLock { stopError = error } }

    func gateStop() -> DispatchSemaphore {
        let gate = DispatchSemaphore(value: 0)
        lock.withLock { stopGate = gate }
        return gate
    }

    func start() throws { lock.withLock { log.append("start") } }

    func stop(within seconds: Double) throws {
        let (gate, error) = lock.withLock {
            log.append("stop")
            return (stopGate, stopError)
        }
        gate?.wait()
        if let error { throw error }
    }
}

private func hostStatus(running: String? = nil, paused: Bool = false) -> HostStatus {
    HostStatus(queued: 0, running: running.map { HostJob(id: $0, project: "p", task: "", state: "running") },
               waitingReason: "", countdownJob: nil, notNowJob: nil, countdownSeconds: nil,
               returnJob: nil, graceSeconds: nil, paused: paused)
}

@MainActor
final class HostModelTests: XCTestCase {
    private func model(_ bridge: FakeBridge, control: FakeControl = FakeControl(),
                       confirm: @escaping @MainActor (Confirmation) -> Bool = { _ in true },
                       sessionStopLimit: Double = 5) -> HostModel {
        HostModel(bridge: bridge, manualReturn: false, verifiedFilter: true, post: { _ in },
                  control: control, confirm: confirm, sessionStopLimit: sessionStopLimit,
                  login: FakeLogin(), startedByAgent: false, sourceInstalled: { false }, bundled: true,
                  exitApp: { XCTFail("the app must not exit") })
    }

    func testRestartReportsAFailedStopAndDoesNotStart() async {
        let control = FakeControl()
        control.failStop(DaemonControlError.stillRunning(seconds: 20))
        let model = model(FakeBridge(), control: control)

        await model.restart()?.value

        XCTAssertEqual(control.calls, ["stop"], "a daemon that didn't stop must not be started")
        XCTAssertEqual(model.actionError, "Restart failed: The daemon didn't stop within 20 seconds.")
        XCTAssertEqual(model.daemon, .notResponding("The daemon didn't stop within 20 seconds."))
        XCTAssertFalse(model.controlling, "the heartbeat resumes")
    }

    func testRestartStartsTheDaemonAfterAStop() async {
        let control = FakeControl()
        let model = model(FakeBridge(), control: control)
        await model.restart()?.value
        XCTAssertEqual(model.daemon, .restarting)
        XCTAssertFalse(model.controlling)
        XCTAssertNil(model.actionError)
    }

    func testQuitAsksBeforeLeavingADaemonThatDidNotStop() async {
        let control = FakeControl()
        control.failStop(DaemonControlError.stillRunning(seconds: 20))
        var asked: [Confirmation] = []
        let declining = model(FakeBridge(), control: control, confirm: { asked.append($0); return false })

        let declined = await declining.quit(systemQuit: false)
        XCTAssertFalse(declined)
        XCTAssertEqual(asked.map(\.confirm), ["Quit Anyway"])
        XCTAssertFalse(declining.controlling, "a declined quit keeps the app running normally")

        let accepting = model(FakeBridge(), control: control, confirm: { _ in true })
        let quit = await accepting.quit(systemQuit: false)
        XCTAssertTrue(quit)
    }

    func testQuitAfterAStopDoesNotAsk() async {
        var asked = 0
        let model = model(FakeBridge(), confirm: { _ in asked += 1; return false })
        let quit = await model.quit(systemQuit: false)
        XCTAssertTrue(quit)
        XCTAssertEqual(asked, 0, "no active jobs and a clean stop need no question")
    }

    func testSessionQuitSkipsCancelsAndIsBounded() async {
        let bridge = FakeBridge()
        bridge.setJobs([DaemonJob(id: "a", state: "running")])
        let control = FakeControl()
        let gate = control.gateStop() // the daemon never exits
        var asked = 0
        let model = model(bridge, control: control, confirm: { _ in asked += 1; return false },
                          sessionStopLimit: 0.2)

        let started = Date()
        let quit = await model.quit(systemQuit: true)
        gate.signal()

        XCTAssertTrue(quit, "logout, restart and shutdown always proceed")
        XCTAssertLessThan(Date().timeIntervalSince(started), 2)
        XCTAssertEqual(asked, 0)
        XCTAssertEqual(bridge.calls.filter { $0.hasPrefix("cancel") }, [], "session quits rely on SIGTERM")
        XCTAssertEqual(control.calls, ["stop"])
    }

    func testSessionQuitDuringARestartIsNotBlockedAndPreventsTheStart() async {
        let control = FakeControl()
        let gate = control.gateStop()
        let model = model(FakeBridge(), control: control, sessionStopLimit: 0.2)

        let restart = model.restart()
        while control.calls.isEmpty { await Task.yield() } // the restart's stop is in flight
        XCTAssertTrue(model.controlling)

        let quit = await model.quit(systemQuit: true)
        XCTAssertTrue(quit, "a restart in progress must not block logout")
        gate.signal()
        await restart?.value

        XCTAssertFalse(control.calls.contains("start"), "the session is ending; the daemon stays stopped")
        XCTAssertTrue(model.controlling, "the heartbeat stays off until the app exits")
    }

    func testAlertsAreDeclinedOnceTheSessionIsEnding() async {
        let bridge = FakeBridge()
        bridge.setJobs([DaemonJob(id: "a", state: "running")])
        var asked = 0
        let model = model(bridge, confirm: { _ in asked += 1; return true }, sessionStopLimit: 0.2)
        _ = await model.quit(systemQuit: true)

        await model.restart()?.value
        XCTAssertEqual(asked, 0)
        let quit = await model.quit(systemQuit: false)
        XCTAssertTrue(quit, "a user quit after logout began still lets the app exit")
        XCTAssertEqual(asked, 0)
    }

    func testClosingTheMenuAcknowledgesFailuresPastTheRecentRows() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus())
        let model = model(bridge)
        await model.refresh()?.value // the first list sets the baseline
        let base = Date(timeIntervalSince1970: 1_000_000)
        var failed = (0..<7).map { DaemonJob(id: "f\($0)", state: "failed", updated: base.addingTimeInterval(Double($0))) }
        bridge.setJobs(failed)
        await model.refresh()?.value
        XCTAssertEqual(model.jobs.recent.count, JobList.recentLimit)
        XCTAssertEqual(model.unseenFailures.count, 7)

        model.menuOpened()
        failed.append(DaemonJob(id: "late", state: "failed", updated: base.addingTimeInterval(10)))
        bridge.setJobs(failed)
        await model.refresh()?.value
        model.menuClosed()

        XCTAssertEqual(model.unseenFailures, ["late"], "only the failure that arrived while open stays unseen")
        XCTAssertEqual(model.iconState, .attention)
        model.menuOpened()
        model.menuClosed()
        XCTAssertEqual(model.unseenFailures, [])
        XCTAssertEqual(model.iconState, .idle)
    }

    func testHeartbeatFailureAndCancelCompletionWaitForTheMenuToClose() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus())
        let job = DaemonJob(id: "a", state: "running")
        bridge.setJobs([job])
        let model = model(bridge)
        await model.refresh()?.value
        let shown = model.jobs
        XCTAssertEqual(shown.active.map(\.id), ["a"])

        model.menuOpened()
        await model.cancel(job)?.value
        XCTAssertEqual(model.cancelling, ["a"], "the open menu keeps its Cancelling… row")
        bridge.setDown(true)
        await model.refresh()?.value
        XCTAssertEqual(model.jobs, shown, "a failed heartbeat must not rebuild the open menu")

        model.menuClosed()
        XCTAssertEqual(model.jobs, JobList(), "the held empty list shows once the menu closes")
        XCTAssertEqual(model.cancelling, [])
        XCTAssertEqual(bridge.calls.filter { $0.hasPrefix("cancel") }, ["cancel a"])
    }

    func testHeartbeatFailureHoldsStatusAndDaemonStateUntilTheMenuCloses() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus())
        bridge.reply("/host/resume", hostStatus(running: "job-1"))
        let model = model(bridge)
        await model.refresh()?.value
        await model.action("resume").value
        let shown = model.status
        XCTAssertEqual(shown?.running?.id, "job-1")
        XCTAssertEqual(model.daemon, .running(version: "test"))

        model.menuOpened()
        bridge.setDown(true)
        await model.refresh()?.value
        XCTAssertEqual(model.status, shown, "a failed heartbeat must not rebuild the open menu's host rows")
        XCTAssertEqual(model.daemon, .running(version: "test"), "nor its daemon line")
        XCTAssertEqual(model.iconState, .running, "the icon follows the menu")
        XCTAssertNil(model.physicalInput(pid: 0), "input must not return a job the daemon no longer confirms")
        XCTAssertFalse(bridge.calls.contains("/host/return/job-1"))

        model.menuClosed()
        XCTAssertNil(model.status)
        XCTAssertEqual(model.daemon, .notResponding(""))
        XCTAssertEqual(model.iconState, .idle)
    }

    func testASuccessfulHeartbeatWhileTheMenuIsOpenReplacesTheHeldFailure() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus())
        let model = model(bridge)
        await model.refresh()?.value

        model.menuOpened()
        bridge.setDown(true)
        await model.refresh()?.value
        bridge.setDown(false)
        bridge.reply("/host/status", hostStatus(paused: true))
        await model.refresh()?.value
        XCTAssertEqual(model.status?.paused, true, "successful replies still update the open menu")

        model.menuClosed()
        XCTAssertEqual(model.status?.paused, true, "the held failure must not clobber a later reply")
        XCTAssertEqual(model.daemon, .running(version: "test"))
    }

    func testAnOlderStatusReplyNeverOverwritesANewerOne() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus(paused: false))
        bridge.reply("/host/pause", hostStatus(paused: true))
        let gate = bridge.gate("/host/status")
        let model = model(bridge)

        let heartbeat = model.refresh()
        // Blocking's queue is concurrent; let the heartbeat reach the daemon first.
        while bridge.calls.isEmpty { await Task.yield() }
        await model.action("pause").value
        XCTAssertEqual(model.status?.paused, true)
        gate.signal() // the heartbeat's older status reply arrives last
        await heartbeat?.value

        XCTAssertEqual(model.status?.paused, true)
        XCTAssertEqual(bridge.calls.first, "/host/status", "the heartbeat asked first")
    }

    func testAutomaticReturnConflictShowsNoErrorAndDoesNotResend() async {
        let bridge = FakeBridge()
        bridge.reply("/host/resume", hostStatus(running: "job-1"))
        bridge.conflict("/host/return/job-1")
        let model = model(bridge)
        await model.action("resume").value

        await model.physicalInput(pid: 0)?.value
        XCTAssertNil(model.actionError)
        XCTAssertEqual(model.iconState, .running)
        for _ in 0..<3 { XCTAssertNil(model.physicalInput(pid: 0), "input must not resend the return") }
        XCTAssertEqual(bridge.calls.filter { $0 == "/host/return/job-1" }.count, 1)

        // A new status clears it: the next running job gets its own return.
        bridge.reply("/host/pause", hostStatus(running: "job-2"))
        bridge.reply("/host/return/job-2", hostStatus(running: "job-2"))
        await model.action("pause").value
        await model.physicalInput(pid: 0)?.value
        XCTAssertEqual(bridge.calls.filter { $0 == "/host/return/job-2" }.count, 1)
        XCTAssertNil(model.actionError)

        // The "I'm back" button still reports its errors.
        await model.action("return", job: "job-1").value
        XCTAssertNotNil(model.actionError)
    }
}
