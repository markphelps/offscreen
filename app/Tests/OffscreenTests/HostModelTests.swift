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

private func hostStatus(running: String? = nil, paused: Bool = false) -> HostStatus {
    HostStatus(queued: 0, running: running.map { HostJob(id: $0, project: "p", task: "", state: "running") },
               waitingReason: "", countdownJob: nil, notNowJob: nil, countdownSeconds: nil,
               returnJob: nil, graceSeconds: nil, paused: paused)
}

@MainActor
final class HostModelTests: XCTestCase {
    private func model(_ bridge: FakeBridge) -> HostModel {
        HostModel(bridge: bridge, manualReturn: false, verifiedFilter: true, post: { _ in })
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

    func testAnOlderStatusReplyNeverOverwritesANewerOne() async {
        let bridge = FakeBridge()
        bridge.reply("/host/status", hostStatus(paused: false))
        bridge.reply("/host/pause", hostStatus(paused: true))
        let gate = bridge.gate("/host/status")
        let model = model(bridge)

        let heartbeat = model.refresh()
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
