import CoreGraphics
import XCTest
@testable import OffscreenHelper

final class HelperTests: XCTestCase {
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
