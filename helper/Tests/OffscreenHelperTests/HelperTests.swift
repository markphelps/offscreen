import CoreGraphics
import XCTest
@testable import OffscreenHelper

final class HelperTests: XCTestCase {
    /// Renders the menu bar icon at Retina scale, one pixel per design unit.
    private func menuBarAlpha(running: Bool) -> (Int, Int) -> CGFloat {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 46, pixelsHigh: 38, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                   colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        rep.size = MenuBarIcon.size
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        MenuBarIcon.image(running: running).draw(in: NSRect(origin: .zero, size: MenuBarIcon.size))
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
        for visible in [NSRect(x: 55, y: 0, width: 3145, height: 1770),
                        NSRect(x: -1920, y: -200, width: 1920, height: 1080),
                        NSRect(x: 0, y: 0, width: 250, height: 100)] {
            let frame = CountdownPlacement.frame(panelSize: NSSize(width: 462, height: 150),
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
        XCTAssertNil(InputFilter.candidatePhysical(pid: 0, runningJob: "job-1", alreadyReturning: false,
                                                   manualReturn: false, verified: false))
        XCTAssertEqual(InputFilter.candidatePhysical(pid: 0, runningJob: "job-1", alreadyReturning: false,
                                                     manualReturn: false, verified: true), "job-1")
        XCTAssertNil(InputFilter.candidatePhysical(pid: 312, runningJob: "job-1", alreadyReturning: false,
                                                   manualReturn: false, verified: true))
        XCTAssertNil(InputFilter.candidatePhysical(pid: 0, runningJob: nil, alreadyReturning: false,
                                                   manualReturn: false, verified: true))
        XCTAssertNil(InputFilter.candidatePhysical(pid: 0, runningJob: "job-1", alreadyReturning: true,
                                                   manualReturn: false, verified: true))
        XCTAssertNil(InputFilter.candidatePhysical(pid: 0, runningJob: "job-1", alreadyReturning: false,
                                                   manualReturn: true, verified: true))
    }

    func testCountdownPanelVisibilityKeepsClickedJobButNotStaleControls() throws {
        func status(_ extra: String) throws -> HostStatus {
            let json = "{\"queued\":1,\"waiting_reason\":\"waiting for idle desktop\",\"paused\":false,\(extra)}"
            return try JSONDecoder().decode(HostStatus.self, from: Data(json.utf8))
        }
        XCTAssertEqual(CountdownVisibility.jobID(status: try status("\"countdown_job\":\"one\""),
                                                 visibleJobID: nil), "one")
        XCTAssertEqual(CountdownVisibility.jobID(status: try status("\"not_now_job\":\"one\""),
                                                 visibleJobID: "one"), "one")
        XCTAssertNil(CountdownVisibility.jobID(status: try status("\"not_now_job\":\"one\""),
                                               visibleJobID: nil))
        XCTAssertEqual(CountdownVisibility.jobID(status: try status("\"countdown_job\":\"two\",\"not_now_job\":\"two\""),
                                                 visibleJobID: "one"), "two")
        let blocked = try JSONDecoder().decode(HostStatus.self,
            from: Data(#"{"queued":1,"waiting_reason":"waiting for exclusive project","paused":false}"#.utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: blocked, visibleJobID: "one"))
        let active = try JSONDecoder().decode(HostStatus.self,
            from: Data(#"{"queued":1,"waiting_reason":"","paused":false,"running":{"id":"one","project":"sample","task":"","state":"preparing"}}"#.utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: active, visibleJobID: "one"))
        let paused = try JSONDecoder().decode(HostStatus.self,
            from: Data(#"{"queued":1,"waiting_reason":"host lane paused","paused":true}"#.utf8))
        XCTAssertNil(CountdownVisibility.jobID(status: paused, visibleJobID: "one"))
    }

    func testSnapshotEncodingUsesDaemonKeys() throws {
        let snapshot = Snapshot(idleSeconds: 300, unlocked: true, displayAwake: true,
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
        let data = Data((#"{"queued":2,"running":{"id":"job-1","project":"sample","task":"test","state":"running"},"#
                         + #""waiting_reason":"idle","return_job":"job-1","grace_seconds":60,"paused":false}"#).utf8)
        let status = try JSONDecoder().decode(HostStatus.self, from: data)
        XCTAssertEqual(status.running?.id, status.returnJob)
        XCTAssertEqual(status.queued, 2)
        XCTAssertEqual(status.graceSeconds, 60)
    }
}
