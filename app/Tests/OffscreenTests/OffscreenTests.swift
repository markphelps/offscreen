import CoreGraphics
import XCTest

@testable import Offscreen

final class OffscreenTests: XCTestCase {
    /// Renders the menu bar icon at Retina scale, one pixel per design unit.
    private func menuBarAlpha(running: Bool, attention: Bool = false) -> (Int, Int) -> CGFloat {
        let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: 46, pixelsHigh: 38, bitsPerSample: 8,
            samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        rep.size = MenuBarIcon.size
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        MenuBarIcon.image(running: running, attention: attention).draw(
            in: NSRect(origin: .zero, size: MenuBarIcon.size))
        NSGraphicsContext.restoreGraphicsState()
        return { x, y in rep.colorAt(x: x, y: y)?.alphaComponent ?? -1 }
    }

    func testMenuBarIconIsATemplateWithACursorOnlyWhileRunning() {
        XCTAssertTrue(MenuBarIcon.image(running: false).isTemplate)
        let idle = menuBarAlpha(running: false)
        let running = menuBarAlpha(running: true)
        for alpha in [idle, running] {
            XCTAssertEqual(alpha(25, 5), 1, "back display is filled")
            XCTAssertEqual(alpha(3, 25), 1, "front display outline")
            XCTAssertEqual(alpha(18, 25), 0, "front display is empty")
            XCTAssertEqual(alpha(36, 18), 0, accuracy: 0.05, "gap beside the front display")
            XCTAssertEqual(alpha(25, 11), 0, accuracy: 0.05, "gap above the front display")
        }
        XCTAssertEqual(idle(40, 7), 1)
        XCTAssertEqual(running(40, 7), 0, "cursor is cut out of the back display")
    }

    func testCameraActiveOrUnknownDevicesCannotBeIdle() {
        XCTAssertEqual(Sensors.cameraIdle(deviceStates: []), true)
        XCTAssertEqual(Sensors.cameraIdle(deviceStates: [0, 0]), true)
        XCTAssertEqual(Sensors.cameraIdle(deviceStates: [1]), false)
        XCTAssertEqual(Sensors.cameraIdle(deviceStates: [0, 2]), false)
        XCTAssertNil(Sensors.cameraIdle(deviceStates: [nil]))
        XCTAssertNil(Sensors.cameraIdle(deviceStates: [0, nil]))
    }

    func testCountdownFrameStaysInsideTheDisplayAfterLayout() {
        for visible in [
            NSRect(x: 55, y: 0, width: 3145, height: 1770),
            NSRect(x: -1920, y: -200, width: 1920, height: 1080),
            NSRect(x: 0, y: 0, width: 250, height: 100),
        ] {
            let frame = CountdownPlacement.frame(
                panelSize: NSSize(width: 462, height: 150),
                visibleFrame: visible)
            XCTAssertGreaterThanOrEqual(frame.minX, visible.minX)
            XCTAssertGreaterThanOrEqual(frame.minY, visible.minY)
            XCTAssertLessThanOrEqual(frame.maxX, visible.maxX)
            XCTAssertLessThanOrEqual(frame.maxY, visible.maxY)
            XCTAssertEqual(frame.midX, visible.midX)
        }
    }

    func testSessionUsesSDKConsoleKeysAndRejectsBlockedSessions() {
        let console = kCGSessionOnConsoleKey as String
        let login = kCGSessionLoginDoneKey as String
        let unlocked: [String: Any] = [console: NSNumber(value: true), login: NSNumber(value: true)]
        XCTAssertTrue(Sensors.sessionUnlocked(unlocked))
        XCTAssertFalse(Sensors.sessionUnlocked(unlocked.merging(["CGSSessionScreenIsLocked": true]) { _, new in new }))
        XCTAssertFalse(Sensors.sessionUnlocked([console: 0, login: 1]))
        XCTAssertFalse(Sensors.sessionUnlocked([console: 1, login: 0]))
        XCTAssertFalse(Sensors.sessionUnlocked([console: 1]))
        XCTAssertFalse(Sensors.sessionUnlocked(nil))
    }
    func testCandidatePhysicalInputRequiresVerifiedFilterAndCurrentJob() {
        XCTAssertNil(
            InputFilter.candidatePhysical(
                pid: 0, runningJob: "job-1", alreadyReturning: false,
                manualReturn: false, verified: false))
        XCTAssertEqual(
            InputFilter.candidatePhysical(
                pid: 0, runningJob: "job-1", alreadyReturning: false,
                manualReturn: false, verified: true), "job-1")
        XCTAssertNil(
            InputFilter.candidatePhysical(
                pid: 312, runningJob: "job-1", alreadyReturning: false,
                manualReturn: false, verified: true))
        XCTAssertNil(
            InputFilter.candidatePhysical(
                pid: 0, runningJob: nil, alreadyReturning: false,
                manualReturn: false, verified: true))
        XCTAssertNil(
            InputFilter.candidatePhysical(
                pid: 0, runningJob: "job-1", alreadyReturning: true,
                manualReturn: false, verified: true))
        XCTAssertNil(
            InputFilter.candidatePhysical(
                pid: 0, runningJob: "job-1", alreadyReturning: false,
                manualReturn: true, verified: true))
    }

    func testCountdownPanelVisibilityKeepsClickedJobButNotStaleControls() throws {
        func status(_ extra: String) throws -> HostStatus {
            let json = "{\"queued\":1,\"waiting_reason\":\"waiting for idle desktop\",\"paused\":false,\(extra)}"
            return try JSONDecoder().decode(HostStatus.self, from: Data(json.utf8))
        }
        XCTAssertEqual(
            CountdownVisibility.jobID(
                status: try status("\"countdown_job\":\"one\""),
                visibleJobID: nil), "one")
        XCTAssertEqual(
            CountdownVisibility.jobID(
                status: try status("\"not_now_job\":\"one\""),
                visibleJobID: "one"), "one")
        XCTAssertNil(
            CountdownVisibility.jobID(
                status: try status("\"not_now_job\":\"one\""),
                visibleJobID: nil))
        XCTAssertEqual(
            CountdownVisibility.jobID(
                status: try status("\"countdown_job\":\"two\",\"not_now_job\":\"two\""),
                visibleJobID: "one"), "two")
        let blocked = try JSONDecoder().decode(
            HostStatus.self,
            from: Data(#"{"queued":1,"waiting_reason":"waiting for exclusive project","paused":false}"#.utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: blocked, visibleJobID: "one"))
        let active = try JSONDecoder().decode(
            HostStatus.self,
            from: Data(
                #"{"queued":1,"waiting_reason":"","paused":false,"running":{"id":"one","project":"sample","task":"","state":"preparing"}}"#
                    .utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: active, visibleJobID: "one"))
        let paused = try JSONDecoder().decode(
            HostStatus.self,
            from: Data(#"{"queued":1,"waiting_reason":"host lane paused","paused":true}"#.utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: paused, visibleJobID: "one"))
    }

    func testHostSensingRunsOnlyWhileTheDaemonHasHostWork() throws {
        func status(_ json: String) throws -> HostStatus {
            try JSONDecoder().decode(HostStatus.self, from: Data(json.utf8))
        }
        let idle = try status(#"{"queued":0,"waiting_reason":"","paused":false}"#)
        XCTAssertFalse(HostSensing.hostActive(idle))
        XCTAssertFalse(HostSensing.hostActive(try status(#"{"queued":0,"waiting_reason":"","paused":true}"#)))
        XCTAssertTrue(
            HostSensing.hostActive(try status(#"{"queued":1,"waiting_reason":"host lane paused","paused":true}"#)))
        XCTAssertTrue(
            HostSensing.hostActive(
                try status(
                    #"{"queued":0,"waiting_reason":"","paused":false,"running":{"id":"one","project":"p","task":"","state":"running"}}"#
                )))
        XCTAssertTrue(
            HostSensing.hostActive(
                try status(
                    #"{"queued":0,"waiting_reason":"","paused":false,"return_job":"one","grace_seconds":30}"#)))
        XCTAssertFalse(HostSensing.needed(hostActive: false, probe: false))
        XCTAssertTrue(HostSensing.needed(hostActive: true, probe: false))
        XCTAssertTrue(HostSensing.needed(hostActive: false, probe: true))
    }

    func testSnapshotEncodingUsesDaemonKeys() throws {
        let snapshot = Snapshot(
            idleSeconds: 300, unlocked: true, displayAwake: true,
            cameraIdle: false, microphoneIdle: true, inputMonitoring: true, eventTapReady: true)
        let json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(snapshot)) as? [String: Any]
        XCTAssertEqual(json?["idle_seconds"] as? Double, 300)
        XCTAssertEqual(json?["display_awake"] as? Bool, true)
        XCTAssertNil(json?["displayAwake"])
        XCTAssertEqual(json?["camera_idle"] as? Bool, false)
        XCTAssertEqual(json?["input_monitoring"] as? Bool, true)
        XCTAssertEqual(json?["event_tap_ready"] as? Bool, true)
    }

    func testHostStatusDecodingKeepsJobIdentity() throws {
        let data = Data(
            (#"{"queued":2,"running":{"id":"job-1","project":"sample","task":"test","state":"running"},"#
                + #""waiting_reason":"idle","return_job":"job-1","grace_seconds":60,"paused":false}"#).utf8)
        let status = try JSONDecoder().decode(HostStatus.self, from: data)
        XCTAssertEqual(status.running?.id, status.returnJob)
        XCTAssertEqual(status.queued, 2)
        XCTAssertEqual(status.graceSeconds, 60)
    }

    func testDaemonPIDComesFromTheServiceLineOnly() {
        let running =
            "gui/501/io.latentsignal.offscreend = {\n\tstate = running\n\tpid = 4242\n\tendpoints = {\n\t\tpid = 7\n\t}\n}\n"
        XCTAssertEqual(DaemonControl.pid(fromPrint: running), 4242)
        let stopped = "gui/501/io.latentsignal.offscreend = {\n\tstate = not running\n\tlast exit code = 0\n}\n"
        XCTAssertNil(DaemonControl.pid(fromPrint: stopped))
    }

    func testDaemonStateFlagsMismatchOnlyBetweenStampedBuilds() {
        XCTAssertEqual(DaemonState.reachable(daemon: "v1-2-gabc", app: "v1-2-gabc"), .running(version: "v1-2-gabc"))
        XCTAssertEqual(
            DaemonState.reachable(daemon: "v1-1-gold", app: "v1-2-gabc"),
            .mismatch(daemon: "v1-1-gold", app: "v1-2-gabc"))
        XCTAssertEqual(DaemonState.reachable(daemon: "dev", app: "v1-2-gabc"), .running(version: "dev"))
        XCTAssertEqual(DaemonState.reachable(daemon: "v1", app: nil), .running(version: "v1"))
        XCTAssertEqual(DaemonState.reachable(daemon: "", app: "v1"), .running(version: "protocol 0"))
        XCTAssertTrue(DaemonState.restarting.busy)
        XCTAssertFalse(DaemonState.notResponding("").busy)
    }

    func testOnlyQueuedAndInFlightJobsAreCancelledAtQuit() throws {
        let json = Data(
            #"[{"id":"a","state":"queued"},{"id":"b","state":"running"},{"id":"c","state":"booting"},{"id":"d","state":"done"},{"id":"e","state":"failed"},{"id":"f","state":"cancelled"}]"#
                .utf8)
        let jobs = try JSONDecoder().decode([DaemonJob].self, from: json)
        XCTAssertEqual(jobs.filter(\.active).map(\.id), ["a", "b", "c"])
        XCTAssertEqual(QuitPrompt.message(verb: "Quit", count: 1), "Quit and cancel 1 job?")
        XCTAssertEqual(QuitPrompt.message(verb: "Quit", count: 2), "Quit and cancel 2 jobs?")
        XCTAssertTrue(QuitPrompt.systemQuit(reason: OSType(kAELogOut)))
        XCTAssertFalse(QuitPrompt.systemQuit(reason: nil))
    }

    func testOnlyTheNewerCopyYieldsToAnotherInstance() {
        let older = SingleInstance.Instance(pid: 200, launched: Date(timeIntervalSince1970: 100))
        let newer = SingleInstance.Instance(pid: 100, launched: Date(timeIntervalSince1970: 200))
        XCTAssertTrue(SingleInstance.yields(newer, to: [older, newer]))
        XCTAssertFalse(SingleInstance.yields(older, to: [older, newer]))
        XCTAssertFalse(SingleInstance.yields(older, to: [older]), "the copy itself doesn't count")

        // Same or missing launch dates fall back to pid, so exactly one stays.
        let agent = SingleInstance.Instance(pid: 50, launched: nil)
        let opened = SingleInstance.Instance(pid: 60, launched: Date())
        XCTAssertTrue(SingleInstance.yields(opened, to: [agent, opened]))
        XCTAssertFalse(SingleInstance.yields(agent, to: [agent, opened]))
    }

    func testMenuBarIconMarksTheFrontDisplayOnlyForAttention() {
        let attention = menuBarAlpha(running: true, attention: true)
        XCTAssertEqual(menuBarAlpha(running: true)(18, 22), 0, "front display is empty")
        XCTAssertEqual(attention(18, 22), 1, "exclamation bar")
        XCTAssertEqual(attention(18, 30), 1, "exclamation dot")
        XCTAssertEqual(attention(18, 28), 0, "gap between bar and dot")
        XCTAssertEqual(attention(40, 7), 0, "cursor still shows")
        XCTAssertEqual(
            MenuBarIcon.image(running: false, attention: true).accessibilityDescription,
            "Offscreen, needs attention")
    }

    func testJobDecodingReadsGoTimesAndTheFieldsTheMenuShows() throws {
        let json = Data(
            #"[{"id":"abcdef0123456789","project":"hello","root":"/r","task":"","lane":"host","state":"failed","code":1,"artifacts":"/a","reason":"exit 1","created":"2026-10-15T12:00:00.123456789-04:00","updated":"2026-10-15T16:01:05Z","deadline":"0001-01-01T00:00:00Z"}]"#
                .utf8)
        let job = try XCTUnwrap(JSONDecoder().decode([DaemonJob].self, from: json).first)
        XCTAssertEqual(job.lane, "host")
        XCTAssertEqual(job.artifacts, "/a")
        XCTAssertEqual(job.name, "hello · abcdef01")
        XCTAssertEqual(job.result, "failed (exit 1)")
        let created = try XCTUnwrap(job.created)
        XCTAssertEqual(try XCTUnwrap(job.updated).timeIntervalSince(created), 65, accuracy: 0.2)
        XCTAssertNil(DaemonJob.date("not a time"))
    }

    func testJobDateParsesPlainAndFractionalRFC3339() throws {
        let plain = try XCTUnwrap(DaemonJob.date("2026-10-15T16:01:05Z"))
        XCTAssertEqual(plain.timeIntervalSince1970, 1_792_080_065)
        let fractional = try XCTUnwrap(DaemonJob.date("2026-10-15T12:01:05.25-04:00"))
        XCTAssertEqual(fractional.timeIntervalSince(plain), 0.25, accuracy: 0.001)
        XCTAssertNil(DaemonJob.date(nil))
    }

    func testJobListSplitsActiveFromTheLastFiveFinished() {
        let base = Date(timeIntervalSince1970: 1_000_000)
        var jobs = (0..<7).map { index in
            DaemonJob(id: "f\(index)", state: "done", created: base, updated: base.addingTimeInterval(Double(index)))
        }
        jobs += [
            DaemonJob(id: "late", state: "running", created: base.addingTimeInterval(10)),
            DaemonJob(id: "early", state: "queued", created: base),
        ]
        let list = JobList(jobs)
        XCTAssertEqual(list.active.map(\.id), ["early", "late"])
        XCTAssertEqual(list.recent.map(\.id), ["f6", "f5", "f4", "f3", "f2"])
        let running = DaemonJob(id: "x", project: "hello", task: "launch", lane: "vm", state: "running", created: base)
        XCTAssertEqual(
            JobList.activeTitle(running, now: base.addingTimeInterval(65)), "hello · launch — vm · running · 1:05")
        XCTAssertEqual(JobList.elapsed(since: base, now: base.addingTimeInterval(3723)), "1:02:03")
        let done = DaemonJob(id: "y", project: "hello", task: "launch", state: "cancelled", updated: base)
        XCTAssertEqual(
            JobList.recentTitle(done, now: base.addingTimeInterval(180)), "hello · launch — vm · cancelled · 3m ago")
        XCTAssertEqual(JobList.ago(base, now: base.addingTimeInterval(5)), "just now")
    }

    func testFailuresNotifyOnceAndNotForJobsFailedBeforeLaunch() {
        var tracker = FailureTracker()
        XCTAssertEqual(
            tracker.newFailures(in: [
                DaemonJob(id: "old", state: "failed"),
                DaemonJob(id: "a", state: "running"),
            ]), [])
        let failed = [
            DaemonJob(id: "old", state: "failed"), DaemonJob(id: "a", state: "failed"),
            DaemonJob(id: "b", state: "cancelled"),
        ]
        XCTAssertEqual(tracker.newFailures(in: failed).map(\.id), ["a"])
        XCTAssertEqual(tracker.newFailures(in: failed), [])
        XCTAssertEqual(tracker.newFailures(in: [DaemonJob(id: "c", state: "failed")]).map(\.id), ["c"])
    }

    func testIconAttentionWinsOverRunningAndIdle() throws {
        let running = [DaemonJob(id: "a", state: "running")]
        XCTAssertEqual(IconState.of(jobs: [], host: nil, unseenFailures: false, actionError: false), .idle)
        XCTAssertEqual(IconState.of(jobs: running, host: nil, unseenFailures: false, actionError: false), .running)
        XCTAssertEqual(IconState.of(jobs: running, host: nil, unseenFailures: true, actionError: false), .attention)
        XCTAssertEqual(IconState.of(jobs: [], host: nil, unseenFailures: false, actionError: true), .attention)
        let countdown = try JSONDecoder().decode(
            HostStatus.self,
            from: Data(
                #"{"queued":1,"waiting_reason":"","countdown_job":"h","countdown_seconds":5,"paused":false}"#.utf8))
        XCTAssertEqual(IconState.of(jobs: [], host: countdown, unseenFailures: false, actionError: false), .attention)
    }
}
