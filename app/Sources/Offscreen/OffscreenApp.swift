import AppKit
import CoreGraphics
import Foundation
import os
import SwiftUI
@preconcurrency import UserNotifications

@MainActor
@Observable final class HostModel {
    var status: HostStatus?
    var actionError: String?
    // Every job, VM and host, from GET /jobs on the same heartbeat.
    var jobs = JobList()
    var now = Date()
    var cancelling: Set<String> = []
    // Failures not yet shown in a menu the user opened and closed; they hold
    // the icon in its attention state.
    private(set) var unseenFailures: Set<String> = []
    private var failures = FailureTracker()
    private var menuObservers: [NSObjectProtocol] = []
    // While the menu is open, job rows keep the list and times they opened
    // with: changing a row's title or submenu during tracking makes AppKit
    // log "rep returned item view with wrong item". The newest list waits
    // here until the menu closes. The heartbeat also writes only values that
    // changed: any write the menu reads rebuilds it, which closes an open
    // job submenu.
    private var openMenus = 0
    private var heldJobs: [DaemonJob]?
    // A failed heartbeat holds the daemon line and host-lane rows the same
    // way. The menu and icon keep the old status, but the model acts only on
    // liveStatus, which is nil while the status is held. A successful reply
    // drops the hold, so closing the menu can't overwrite it.
    private var statusHeld = false
    private var heldDaemon: DaemonState?
    // Failures unseen when the menu opened; closing it acknowledges them all,
    // including ones past the recent rows.
    private var acknowledgeable: Set<String> = []
    // Cancels that finished while the menu was open.
    private var cancelsDone: Set<String> = []
    // Host status replies apply in request order: a heartbeat fetches jobs
    // after its status, so an action reply can land in between.
    private var statusRequests = 0
    private var appliedStatus = 0

    /// The status the daemon last confirmed, which input and the countdown
    /// panel act on. The menu shows status.
    private var liveStatus: HostStatus? { statusHeld ? nil : status }

    var iconState: IconState {
        IconState.of(jobs: jobs.active, host: status, unseenFailures: !unseenFailures.isEmpty,
                     actionError: actionError != nil)
    }
    // The app owns the daemon: it starts it at launch and stops it at quit.
    var daemon: DaemonState = .starting
    private let control: any DaemonLauncher
    private let confirm: @MainActor (Confirmation) async -> Bool
    private let alert = ConfirmationAlert()
    // Logout, restart and shutdown wait this long for the daemon to exit,
    // so loginwindow never gives up on the app.
    private let sessionStopLimit: Double
    // Set once a logout, restart or shutdown quits the app.
    private var sessionEnding = false
    private static let log = Logger(subsystem: "io.latentsignal.offscreen", category: "daemon")
    private var daemonVersion: DaemonVersion?
    // A daemon that is starting gets this long to answer before the menu
    // says it isn't responding. start() sets it before the first heartbeat.
    private var startDeadline = Date.distantPast
    // Set while quit or restart cancels jobs and stops the daemon, so the
    // heartbeat doesn't report the stop as a failure.
    private(set) var controlling = false
    private let appBuild = Bundle.main.object(forInfoDictionaryKey: "OffscreenBuild") as? String
    private var countdownPanel: CountdownPanel?
    private let bridge: any HostBridge
    private let post: @MainActor (UNNotificationRequest) -> Void
    private var timer: Timer?
    private var eventTap: CFMachPort?
    private var tapSource: CFRunLoopSource?
    // Input Monitoring, the event tap and snapshots run only while the daemon
    // has host work, so VM-only users are never asked for the permission.
    private var hostActive = false
    private var sensing = false
    // Held while sensing so App Nap can't delay the 1s heartbeat past the
    // daemon's 3s freshness limit.
    private var napActivity: NSObjectProtocol?
    private var previousNotice = ""
    private var refreshing = false
    private var refreshAfterInput = false
    private var pendingReturn = ""
    private let probe = CommandLine.arguments.contains("--probe-input")
    private let manualReturn: Bool
    private let verifiedFilter: Bool

    init(bridge: any HostBridge = Bridge(socketPath: Bridge.defaultSocket()),
         manualReturn: Bool = UserDefaults.standard.bool(forKey: "ManualReturn"),
         verifiedFilter: Bool = UserDefaults.standard.bool(forKey: "VerifiedInputFilter"),
         post: @escaping @MainActor (UNNotificationRequest) -> Void = Notifier.post,
         control: any DaemonLauncher = DaemonControl(),
         confirm: (@MainActor (Confirmation) async -> Bool)? = nil,
         sessionStopLimit: Double = 5) {
        self.bridge = bridge
        self.control = control
        self.confirm = confirm ?? { [alert] in await alert.ask($0) }
        self.sessionStopLimit = sessionStopLimit
        self.manualReturn = manualReturn
        self.verifiedFilter = verifiedFilter
        self.post = post
    }

    func start() {
        guard timer == nil else { return }
        let center = NotificationCenter.default
        menuObservers.append(center.addObserver(forName: NSMenu.didBeginTrackingNotification,
                                                object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.menuOpened() }
        })
        menuObservers.append(center.addObserver(forName: NSMenu.didEndTrackingNotification,
                                                object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.menuClosed() }
        })
        // Logout can start while a Restart or Quit alert is up. Its quit
        // event may wait for the alert, so close it now rather than block
        // logout. Only the alert is closed: logout can still be cancelled.
        menuObservers.append(NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.willPowerOffNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.closePrompt() }
        })
        startDaemon(.starting)
        let heartbeat = Timer(timeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refresh() }
        }
        RunLoop.main.add(heartbeat, forMode: .common)
        timer = heartbeat
        refresh()
    }

    private func startDaemon(_ state: DaemonState) {
        setDaemon(state)
        daemonVersion = nil
        startDeadline = Date().addingTimeInterval(15)
        let control = self.control
        Task {
            do {
                try await Blocking.run { try control.start() }
            } catch {
                // A restart's start can finish after logout began.
                guard !sessionEnding else { return }
                startDeadline = .distantPast
                setDaemon(.notResponding("\(error)"))
            }
        }
    }

    @discardableResult
    func refresh() -> Task<Void, Never>? {
        guard !refreshing, !controlling else { return nil }
        refreshing = true
        let request = nextStatusRequest()
        let needVersion = daemonVersion == nil
        let sense = HostSensing.needed(hostActive: hostActive, probe: probe)
        setSensing(sense)
        let tapReady = eventTap.map { CFMachPortIsValid($0) && CGEvent.tapIsEnabled(tap: $0) } ?? false
        let bridge = self.bridge
        return Task {
            do {
                let (state, jobs, version) = try await Blocking.run { () throws -> (HostStatus, [DaemonJob], DaemonVersion?) in
                    let state = sense
                        ? try bridge.request("/host/snapshot", snapshot: Sensors.snapshot(eventTapReady: tapReady))
                        : try bridge.request("/host/status", snapshot: nil)
                    return (state, try bridge.jobs(), needVersion ? try bridge.version() : nil)
                }
                refreshing = false
                // A reply that raced a quit or restart is stale.
                guard !controlling else { return }
                if let version { daemonVersion = version }
                if let current = daemonVersion {
                    heldDaemon = nil
                    assign(\.daemon, DaemonState.reachable(daemon: current.version, app: appBuild))
                }
                apply(state, request: request)
                hostActive = HostSensing.hostActive(status ?? state)
                updateJobs(jobs)
                // Send the first snapshot now rather than on the next
                // heartbeat so admission isn't delayed by the switch.
                if refreshAfterInput || HostSensing.needed(hostActive: hostActive, probe: probe) != sense {
                    refreshAfterInput = false
                    refresh()
                }
            } catch {
                refreshing = false
                refreshAfterInput = false
                guard !controlling else { return }
                // No daemon, no host job: stop sensing and release the App
                // Nap activity until it answers again.
                hostActive = false
                setJobs([])
                countdownPanel?.hide()
                daemonVersion = nil
                if openMenus > 0 { statusHeld = true } else { assign(\.status, nil) }
                if case .notResponding = heldDaemon ?? daemon { return }
                guard Date() >= startDeadline else { return }
                if openMenus > 0 { heldDaemon = .notResponding("") } else { daemon = .notResponding("") }
            }
        }
    }

    /// Sets the daemon state now, dropping one a failed heartbeat held.
    private func setDaemon(_ state: DaemonState) {
        heldDaemon = nil
        daemon = state
    }

    private func nextStatusRequest() -> Int {
        statusRequests += 1
        return statusRequests
    }

    /// Applies a status reply unless a later request's reply already applied.
    private func apply(_ new: HostStatus, request: Int) {
        guard request > appliedStatus else { return }
        appliedStatus = request
        update(new)
    }

    private func update(_ new: HostStatus) {
        statusHeld = false
        assign(\.status, new)
        updateCountdownPanel(new)
        if let id = new.returnJob, id != previousNotice {
            previousNotice = id
            let notification = UNMutableNotificationContent()
            let name = new.running.map { "\($0.project) · \($0.task.isEmpty ? $0.id : $0.task)" } ?? id
            notification.title = "Host job is using your desktop"
            notification.body = "\(name) continues for \(new.graceSeconds ?? 0)s. Take back now to stop and requeue it."
            notification.categoryIdentifier = "HOST_RETURN"
            let request = UNNotificationRequest(identifier: "return-\(id)", content: notification, trigger: nil)
            post(request)
        }
        if new.returnJob == nil { previousNotice = "" }
        if pendingReturn != new.running?.id || new.returnJob != nil { pendingReturn = "" }
    }

    func menuOpened() {
        if openMenus == 0 { acknowledgeable = unseenFailures }
        openMenus += 1
    }

    /// Closing the menu acknowledges the failures known when it opened, even
    /// ones past the recent rows; ones that arrived while it was open keep
    /// the icon's attention state.
    func menuClosed() {
        openMenus = max(0, openMenus - 1)
        guard openMenus == 0 else { return }
        assign(\.unseenFailures, unseenFailures.subtracting(acknowledgeable))
        acknowledgeable = []
        assign(\.cancelling, cancelling.subtracting(cancelsDone))
        cancelsDone = []
        if let list = heldJobs {
            heldJobs = nil
            showJobs(list)
        }
        if statusHeld {
            statusHeld = false
            assign(\.status, nil)
        }
        if let state = heldDaemon {
            heldDaemon = nil
            assign(\.daemon, state)
        }
    }

    private func showJobs(_ list: [DaemonJob]) {
        now = Date()
        assign(\.jobs, JobList(list))
    }

    private func setJobs(_ list: [DaemonJob]) {
        if openMenus > 0 { heldJobs = list } else { showJobs(list) }
    }

    private func updateJobs(_ list: [DaemonJob]) {
        setJobs(list)
        let ids = Set(list.map(\.id))
        assign(\.unseenFailures, unseenFailures.intersection(ids))
        assign(\.cancelling, cancelling.intersection(ids))
        for job in failures.newFailures(in: list) {
            unseenFailures.insert(job.id)
            notifyFailure(job)
        }
    }

    private func notifyFailure(_ job: DaemonJob) {
        let notification = UNMutableNotificationContent()
        notification.title = "Job failed: \(job.name)"
        notification.subtitle = "\(job.lane) lane · \(job.result)"
        notification.body = job.reason.isEmpty ? "Click to reveal its artifacts." : job.reason
        notification.userInfo = ["artifacts": job.artifacts]
        let request = UNNotificationRequest(identifier: "failed-\(job.id)", content: notification, trigger: nil)
        post(request)
    }

    /// Cancels through the daemon's normal path; the call returns once the
    /// job stopped and its clone is gone, so it runs off the main actor.
    @discardableResult
    func cancel(_ job: DaemonJob) -> Task<Void, Never>? {
        guard !cancelling.contains(job.id) else { return nil }
        cancelling.insert(job.id)
        let bridge = self.bridge
        return Task {
            do {
                try await Blocking.run { try bridge.cancel(job.id) }
                // The row says "Cancelling…" until the menu closes rather
                // than rebuilding the open menu.
                if openMenus > 0 { cancelsDone.insert(job.id) } else { cancelling.remove(job.id) }
            } catch {
                cancelling.remove(job.id)
                actionError = "Cancel \(job.name) failed: \(error)"
            }
        }
    }

    func revealArtifacts(_ path: String) {
        guard !path.isEmpty, FileManager.default.fileExists(atPath: path) else {
            actionError = "Artifacts not found: \(path.isEmpty ? "no directory" : path)"
            return
        }
        NSWorkspace.shared.open(URL(fileURLWithPath: path, isDirectory: true))
    }

    func copyID(_ id: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(id, forType: .string)
    }

    private func updateCountdownPanel(_ current: HostStatus) {
        // The click itself resets HID idle. Keep the button while the daemon
        // still has this queued job's idle-reset action, but never for a
        // replaced, admitted, cancelled, paused, or otherwise blocked job.
        guard let id = CountdownVisibility.jobID(status: current, visibleJobID: countdownPanel?.jobID) else {
            countdownPanel?.hide()
            countdownPanel = nil
            return
        }
        if countdownPanel?.jobID != id {
            countdownPanel?.hide()
            countdownPanel = CountdownPanel(jobID: id) { [weak self] jobID in
                self?.action("not-now", job: jobID)
            }
        }
        countdownPanel?.show(seconds: current.countdownJob == id ? current.countdownSeconds : nil,
                             error: actionError)
    }

    func dismissActionError() {
        actionError = nil
        if let liveStatus { updateCountdownPanel(liveStatus) }
    }

    /// Runs a host action. An automatic return is one the event tap sent
    /// rather than the "I'm back" button.
    @discardableResult
    func action(_ name: String, job: String = "", automatic: Bool = false) -> Task<Void, Never> {
        let bridge = self.bridge
        let request = nextStatusRequest()
        return Task {
            do {
                let state = try await Blocking.run {
                    try bridge.request("/host/\(name)" + (job.isEmpty ? "" : "/\(job)"), snapshot: nil)
                }
                actionError = nil
                apply(state, request: request)
            } catch BridgeError.rejected(409, _) where automatic {
                // The job finished before the return reached the daemon.
                // pendingReturn stays set, so input doesn't resend until the
                // status no longer shows the job running.
            } catch {
                if name == "return", pendingReturn == job { pendingReturn = "" }
                actionError = "\(name == "not-now" ? "Not now" : name == "take-back" ? "Take back" : "Host action") failed: \(error)"
                if let liveStatus { updateCountdownPanel(liveStatus) }
            }
        }
    }
}

// Quit, Restart Daemon and session end.
extension HostModel {
    /// Restart Daemon: the same confirm-and-cancel as quit, then a new
    /// process. A daemon that didn't stop is reported, not restarted.
    @discardableResult
    func restart() -> Task<Void, Never>? {
        guard !controlling else { return nil }
        return Task {
            switch await shutDown(verb: "Restart") {
            case .declined:
                break
            case .failed(let error):
                actionError = "Restart failed: \(error)"
                finishControl()
            case .stopped:
                guard !sessionEnding else { return }
                startDaemon(.restarting)
                controlling = false
            }
        }
    }

    /// Quit Offscreen. Returns whether the app may terminate. A user quit asks
    /// before leaving a daemon that didn't stop; a session quit never asks.
    func quit(systemQuit: Bool) async -> Bool {
        if systemQuit { return await endSession() }
        switch await shutDown(verb: "Quit") {
        case .stopped:
            return true
        case .declined:
            // A session quit that arrived meanwhile closed the alert.
            return sessionEnding
        case .failed(let error):
            let quit = await ask(Confirmation(message: "The daemon didn't stop",
                                        detail: "\(error)\nQuitting Offscreen leaves it running.",
                                        confirm: "Quit Anyway", decline: "Don't Quit"))
            if !quit { finishControl() }
            return quit || sessionEnding
        }
    }

    /// Logout, restart or shutdown: sends the daemon SIGTERM and waits at
    /// most sessionStopLimit for it to exit. It skips the cancel passes,
    /// which can take minutes; the daemon fails its in-flight jobs and
    /// deletes their clones the next time it starts. It doesn't wait for a
    /// restart in progress, and stops one from starting the daemon again.
    func endSession() async -> Bool {
        sessionEnding = true
        controlling = true
        closePrompt()
        setDaemon(.stopping)
        countdownPanel?.hide()
        let control = self.control
        let limit = sessionStopLimit
        let result = await Blocking.run(within: limit) { () -> String? in
            do {
                try control.stop(within: limit)
                return nil
            } catch {
                return "\(error)"
            }
        }
        switch result {
        case nil: Self.log.warning("daemon didn't stop within \(limit)s of session end")
        case let error??: Self.log.warning("daemon stop at session end failed: \(error, privacy: .public)")
        case .some(nil): break
        }
        return true
    }

    enum ShutdownResult: Equatable {
        case stopped
        case declined
        case failed(String)
    }

    /// Cancels active jobs through the daemon's cancel path, so VM clones are
    /// deleted and host guardians stop, then stops the daemon. Asks first if
    /// jobs are active. A failed stop leaves `controlling` set for the caller
    /// to clear.
    func shutDown(verb: String) async -> ShutdownResult {
        controlling = true
        let bridge = self.bridge
        let active = ((try? await Blocking.run { try bridge.jobs() }) ?? []).filter(\.active)
        if !active.isEmpty {
            let detail = "\(verb == "Quit" ? "Quitting Offscreen" : "Restarting") stops the daemon. Cancelled jobs stop and their VM clones are deleted."
            guard await ask(Confirmation(message: QuitPrompt.message(verb: verb, count: active.count), detail: detail,
                                   confirm: "\(verb) and Cancel Jobs", decline: "Don't \(verb)")) else {
                finishControl()
                return .declined
            }
        }
        setDaemon(verb == "Quit" ? .stopping : .restarting)
        // Repeat in case a job was submitted while the first pass ran.
        var failures: [String] = []
        for _ in 0..<3 {
            let jobs = ((try? await Blocking.run { try bridge.jobs() }) ?? []).filter(\.active)
            if jobs.isEmpty { break }
            failures = await withTaskGroup(of: String?.self) { group in
                for job in jobs {
                    group.addTask {
                        do {
                            try await Blocking.run { try bridge.cancel(job.id) }
                            return nil
                        } catch {
                            return "\(job.id): \(error)"
                        }
                    }
                }
                var failed: [String] = []
                for await failure in group { if let failure { failed.append(failure) } }
                return failed
            }
            if !failures.isEmpty { break }
        }
        if !failures.isEmpty {
            guard await ask(Confirmation(message: "Some jobs couldn't be cancelled", detail: failures.joined(separator: "\n"),
                                   confirm: "\(verb) Anyway", decline: "Don't \(verb)")) else {
                finishControl()
                return .declined
            }
        }
        let control = self.control
        do {
            try await Blocking.run { try control.stop(within: 20) }
        } catch {
            setDaemon(.notResponding("\(error)"))
            return .failed("\(error)")
        }
        statusHeld = false
        status = nil
        jobs = JobList()
        countdownPanel?.hide()
        return .stopped
    }

    /// Lets the heartbeat run again, unless the session is ending.
    private func finishControl() {
        if !sessionEnding { controlling = false }
    }

    /// Asks with the confirm closure. Declines without asking once the
    /// session is ending.
    private func ask(_ question: Confirmation) async -> Bool {
        guard !sessionEnding else { return false }
        return await confirm(question) && !sessionEnding
    }

    /// Closes an open Restart or Quit alert as declined.
    private func closePrompt() {
        alert.dismiss()
    }
}

/// The Restart and Quit confirmations. The alert is shown as an ordinary
/// window, not with runModal: during a modal session AppKit cancels the quit
/// event that logout sends without asking the delegate, which interrupts
/// logout.
@MainActor final class ConfirmationAlert: NSObject {
    private var shown: NSAlert?
    private var answer: CheckedContinuation<Bool, Never>?

    func ask(_ question: Confirmation) async -> Bool {
        dismiss()
        let alert = NSAlert()
        alert.messageText = question.message
        alert.informativeText = question.detail
        let confirm = alert.addButton(withTitle: question.confirm)
        let decline = alert.addButton(withTitle: question.decline)
        confirm.target = self
        confirm.action = #selector(confirmed)
        decline.target = self
        decline.action = #selector(declined)
        decline.keyEquivalent = "\u{1b}"
        alert.layout()
        alert.window.level = .modalPanel
        alert.window.center()
        shown = alert
        // A menu bar app is never frontmost on its own; the alert must be.
        NSApp.activate()
        alert.window.makeKeyAndOrderFront(nil)
        return await withCheckedContinuation { answer = $0 }
    }

    /// Closes the alert, if one is up, as declined.
    func dismiss() { finish(false) }

    @objc private func confirmed() { finish(true) }
    @objc private func declined() { finish(false) }

    private func finish(_ result: Bool) {
        shown?.window.orderOut(nil)
        shown = nil
        let pending = answer
        answer = nil
        pending?.resume(returning: result)
    }
}

// Sensing: Input Monitoring, the event tap and the physical input it reports.
extension HostModel {
    private func setSensing(_ on: Bool) {
        guard on else {
            if sensing { removeTap() }
            sensing = false
            if let activity = napActivity {
                ProcessInfo.processInfo.endActivity(activity)
                napActivity = nil
            }
            return
        }
        if !sensing {
            sensing = true
            _ = CGRequestListenEventAccess()
        }
        if napActivity == nil {
            // Keeps timers on time without preventing idle system sleep.
            napActivity = ProcessInfo.processInfo.beginActivity(
                options: [.userInitiatedAllowingIdleSystemSleep, .latencyCritical],
                reason: "Sending the host heartbeat while sensing")
        }
        if let tap = eventTap, !CFMachPortIsValid(tap) { removeTap() }
        // A denied grant can be enabled in System Settings without restarting.
        if eventTap == nil && CGPreflightListenEventAccess() { installTap() }
    }

    private func removeTap() {
        if let source = tapSource { CFRunLoopRemoveSource(CFRunLoopGetMain(), source, .commonModes) }
        if let tap = eventTap { CFMachPortInvalidate(tap) }
        tapSource = nil
        eventTap = nil
    }

    private func installTap() {
        let kinds: [CGEventType] = [.keyDown, .mouseMoved, .leftMouseDown, .rightMouseDown, .scrollWheel]
        let mask = kinds.reduce(CGEventMask(0)) { $0 | (CGEventMask(1) << $1.rawValue) }
        guard let tap = CGEvent.tapCreate(tap: .cgSessionEventTap, place: .headInsertEventTap,
                                          options: .listenOnly, eventsOfInterest: mask,
                                          callback: { _, type, event, refcon in
            guard let refcon else { return Unmanaged.passUnretained(event) }
            let model = Unmanaged<HostModel>.fromOpaque(refcon).takeUnretainedValue()
            if type == .tapDisabledByTimeout || type == .tapDisabledByUserInput {
                if let tap = model.eventTap { CGEvent.tapEnable(tap: tap, enable: true) }
                return Unmanaged.passUnretained(event)
            }
            model.observed(type: type, event: event)
            return Unmanaged.passUnretained(event)
        }, userInfo: Unmanaged.passUnretained(self).toOpaque()) else { return }
        eventTap = tap
        let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0)
        tapSource = source
        CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
        CGEvent.tapEnable(tap: tap, enable: true)
    }

    private func observed(type: CGEventType, event: CGEvent) {
        // Report mouse-down immediately instead of waiting for the heartbeat:
        // the countdown must stop before the button's mouse-up.
        if type == .leftMouseDown, liveStatus?.notNowJob != nil {
            if refreshing { refreshAfterInput = true } else { refresh() }
        }
        let pid = event.getIntegerValueField(.eventSourceUnixProcessID)
        let state = event.getIntegerValueField(.eventSourceStateID)
        if probe { print("type=\(type.rawValue) pid=\(pid) sourceState=\(state)"); fflush(stdout) }
        physicalInput(pid: pid)
    }

    /// Starts the grace period when input that looks physical arrives while a
    /// host job runs.
    @discardableResult
    func physicalInput(pid: Int64) -> Task<Void, Never>? {
        // Candidate only: physical-vs-injected behavior requires the human probe.
        // Never consider injected events (which reset HIDIdleTime) a return.
        let running: String? = ["preparing", "running", "collecting"].contains(liveStatus?.running?.state ?? "")
            ? liveStatus?.running?.id : nil
        let returning = liveStatus?.returnJob != nil || pendingReturn == running
        guard let id = InputFilter.candidatePhysical(pid: pid, runningJob: running,
                                                     alreadyReturning: returning,
                                                     manualReturn: manualReturn,
                                                     verified: verifiedFilter) else { return nil }
        pendingReturn = id
        return action("return", job: id, automatic: true)
    }
}

extension HostModel {
    /// Writes only a changed value, so observers such as the open menu don't
    /// rebuild for nothing. Passing the property inout would not do: that
    /// goes through the observable modify accessor, which always notifies.
    fileprivate func assign<T: Equatable>(_ key: ReferenceWritableKeyPath<HostModel, T>, _ value: T) {
        if self[keyPath: key] != value { self[keyPath: key] = value }
    }
}

enum Notifier {
    private static let log = Logger(subsystem: "io.latentsignal.offscreen", category: "notifications")

    /// Asks for Notifications only when there is one to show. After the first
    /// answer macOS returns it without prompting again.
    /// UNUserNotificationCenter crashes outside an app bundle, as under
    /// `swift run`.
    static var available: Bool { Bundle.main.bundleIdentifier != nil }

    static func post(_ request: UNNotificationRequest) {
        guard available else {
            log.info("notification \(request.identifier, privacy: .public) not shown: not running from an app bundle")
            return
        }
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert, .sound]) { granted, error in
            guard granted else {
                log.warning("notification \(request.identifier, privacy: .public) not shown: \(error?.localizedDescription ?? "not allowed", privacy: .public)")
                return
            }
            center.add(request) { error in
                if let error { log.warning("notification \(request.identifier, privacy: .public) failed: \(error.localizedDescription, privacy: .public)") }
            }
        }
    }
}

enum HostSensing {
    /// The daemon has host work: a queued or running job, or a grace period.
    static func hostActive(_ status: HostStatus) -> Bool {
        status.queued > 0 || status.running != nil || status.returnJob != nil
    }

    /// Probe mode always listens so it can compare physical and injected input.
    static func needed(hostActive: Bool, probe: Bool) -> Bool { probe || hostActive }
}

enum CountdownVisibility {
    static func jobID(status: HostStatus, visibleJobID: String?) -> String? {
        guard status.running == nil, !status.paused, status.queued > 0 else { return nil }
        // The server keeps this job-bound action after HID idle resets on
        // mouse-down. An unrelated queued job cannot inherit the old button.
        let id = status.notNowJob ?? status.countdownJob
        return id == visibleJobID || status.countdownJob == id ? id : nil
    }
}

enum CountdownPlacement {
    static func frame(panelSize: NSSize, visibleFrame: NSRect) -> NSRect {
        let width = min(panelSize.width, visibleFrame.width)
        let height = min(panelSize.height, visibleFrame.height)
        return NSRect(x: visibleFrame.midX - width / 2,
                      y: max(visibleFrame.minY, visibleFrame.maxY - height - 16),
                      width: width, height: height)
    }
}

@MainActor private final class CountdownPanel {
    let jobID: String
    private let panel: NonactivatingCountdownPanel
    private let label: NSTextField
    private let detail: NSTextField
    private let action: CountdownAction

    init(jobID: String, onNotNow: @escaping (String) -> Void) {
        self.jobID = jobID
        action = CountdownAction(jobID: jobID, onNotNow: onNotNow)
        panel = NonactivatingCountdownPanel(contentRect: NSRect(x: 0, y: 0, width: 300, height: 122),
                                            styleMask: [.titled, .nonactivatingPanel],
                                            backing: .buffered, defer: false)
        label = NSTextField(labelWithString: "Host job starting")
        detail = NSTextField(labelWithString: "")
        panel.title = "Offscreen"
        panel.level = .floating
        panel.hidesOnDeactivate = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        label.font = .systemFont(ofSize: NSFont.systemFontSize, weight: .semibold)
        detail.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        detail.textColor = .secondaryLabelColor
        detail.lineBreakMode = .byWordWrapping
        detail.maximumNumberOfLines = 2
        detail.preferredMaxLayoutWidth = 268
        let button = CountdownButton(title: "Not now (30 minutes)", target: action,
                                     action: #selector(CountdownAction.notNow))
        button.bezelStyle = .rounded
        button.setAccessibilityLabel("Not now: pause host jobs for 30 minutes")
        let stack = NSStackView(views: [label, detail, button])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10
        stack.translatesAutoresizingMaskIntoConstraints = false
        panel.contentView?.addSubview(stack)
        if let content = panel.contentView {
            NSLayoutConstraint.activate([
                stack.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: 16),
                stack.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -16),
                stack.topAnchor.constraint(equalTo: content.topAnchor, constant: 12),
                stack.bottomAnchor.constraint(lessThanOrEqualTo: content.bottomAnchor, constant: -12)
            ])
        }
    }

    func show(seconds: Int?, error: String?) {
        label.stringValue = seconds.map { "Host job starts in \($0) seconds" } ?? "Host job waiting for idle desktop"
        detail.stringValue = error ?? "Use Not now to pause host jobs before they take the screen."
        if !panel.isVisible {
            let screen = NSScreen.screens.first { $0.frame.contains(NSEvent.mouseLocation) }
                ?? NSScreen.main ?? NSScreen.screens.first
            if let screen {
                panel.layoutIfNeeded()
                panel.setFrame(CountdownPlacement.frame(panelSize: panel.frame.size,
                                                         visibleFrame: screen.visibleFrame), display: true)
            }
            panel.orderFrontRegardless() // no activation or key-window request
        }
    }

    func hide() { panel.orderOut(nil) }
}

private final class NonactivatingCountdownPanel: NSPanel {
    override var canBecomeKey: Bool { false }
    override var canBecomeMain: Bool { false }
}

private final class CountdownButton: NSButton {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}

@MainActor private final class CountdownAction: NSObject {
    let jobID: String
    let onNotNow: (String) -> Void

    init(jobID: String, onNotNow: @escaping (String) -> Void) {
        self.jobID = jobID
        self.onNotNow = onNotNow
    }

    @objc func notNow() { onNotNow(jobID) }
}

final class NotificationHandler: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    static let shared = NotificationHandler()
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                             willPresent notification: UNNotification,
                                             withCompletionHandler completionHandler: @escaping @Sendable (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                             didReceive response: UNNotificationResponse,
                                             withCompletionHandler completionHandler: @escaping () -> Void) {
        let identifier = response.notification.request.identifier
        if identifier.hasPrefix("return-"), response.actionIdentifier == "TAKE_BACK" {
            let id = String(identifier.dropFirst("return-".count))
            Task { @MainActor in AppState.model.action("take-back", job: id) }
        } else if identifier.hasPrefix("failed-"), response.actionIdentifier == UNNotificationDefaultActionIdentifier,
                  let path = response.notification.request.content.userInfo["artifacts"] as? String {
            Task { @MainActor in AppState.model.revealArtifacts(path) }
        }
        completionHandler()
    }
}

@MainActor enum AppState { static let model = HostModel() }

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate {
    // A terminateLater reply is still owed.
    private var replyPending = false
    private var powerOffObserver: NSObjectProtocol?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // A user Quit can still be cancelling jobs, for minutes, when logout
        // starts. AppKit doesn't ask again while a reply is pending, so end
        // the session now rather than hold up logout.
        powerOffObserver = NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.willPowerOffNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, self.replyPending else { return }
                Task {
                    _ = await AppState.model.endSession()
                    self.reply(NSApp, quit: true)
                }
            }
        }
    }

    private func reply(_ sender: NSApplication, quit: Bool) {
        guard replyPending else { return }
        replyPending = false
        sender.reply(toApplicationShouldTerminate: quit)
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        let model = AppState.model
        let reason = NSAppleEventManager.shared().currentAppleEvent?
            .attributeDescriptor(forKeyword: AEKeyword(kAEQuitReason))?.enumCodeValue
        let systemQuit = QuitPrompt.systemQuit(reason: reason)
        // A user quit waits for a restart in progress; logout never does.
        if model.controlling && !systemQuit { return .terminateCancel }
        replyPending = true
        Task {
            let quit = await model.quit(systemQuit: systemQuit)
            reply(sender, quit: quit)
        }
        return .terminateLater
    }
}

/// Queued and running jobs from both lanes, then the last finished ones,
/// each with a submenu of its actions.
struct JobsMenu: View {
    let model: HostModel

    var body: some View {
        Text("Jobs")
        if model.jobs.active.isEmpty { Text("No queued or running jobs") }
        ForEach(model.jobs.active, id: \.id) { job in
            Menu(JobList.activeTitle(job, now: model.now)) {
                if !job.reason.isEmpty { Text(job.reason) }
                Button(model.cancelling.contains(job.id) ? "Cancelling…" : "Cancel") { model.cancel(job) }
                    .disabled(model.cancelling.contains(job.id))
                Button("Copy Job ID") { model.copyID(job.id) }
            }
        }
        if !model.jobs.recent.isEmpty {
            Divider()
            Text("Recently finished")
            ForEach(model.jobs.recent, id: \.id) { job in
                Menu(JobList.recentTitle(job, now: model.now)) {
                    if !job.reason.isEmpty { Text(job.reason) }
                    Button("Reveal Artifacts") { model.revealArtifacts(job.artifacts) }
                        .disabled(job.artifacts.isEmpty)
                    Button("Copy Job ID") { model.copyID(job.id) }
                }
            }
        }
    }
}

@main struct OffscreenApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    init() {
        Self.exitIfAlreadyRunning()
        if Notifier.available {
            let action = UNNotificationAction(identifier: "TAKE_BACK", title: "Take back now", options: [])
            let category = UNNotificationCategory(identifier: "HOST_RETURN", actions: [action], intentIdentifiers: [])
            UNUserNotificationCenter.current().setNotificationCategories([category])
            UNUserNotificationCenter.current().delegate = NotificationHandler.shared
        }
        Task { @MainActor in AppState.model.start() }
    }

    /// Exits 0 if an older copy is running. Zero matters: the source-install
    /// LaunchAgent restarts the app only after a failed exit.
    private static func exitIfAlreadyRunning() {
        guard let id = Bundle.main.bundleIdentifier else { return }
        // Launched outside LaunchServices, as the source-install LaunchAgent
        // does, this copy isn't registered yet: NSRunningApplication.current
        // has pid -1 and no launch date. It is starting now.
        let me = SingleInstance.Instance(pid: getpid(), launched: NSRunningApplication.current.launchDate ?? Date())
        let others = NSRunningApplication.runningApplications(withBundleIdentifier: id)
            .filter { !$0.isTerminated }
            .map { SingleInstance.Instance(pid: $0.processIdentifier, launched: $0.launchDate) }
        if SingleInstance.yields(me, to: others) { exit(0) }
    }

    var body: some Scene {
        MenuBarExtra {
            let model = AppState.model
            Text(model.daemon.summary)
            if case .notResponding(let detail) = model.daemon, !detail.isEmpty { Text(detail) }
            Divider()
            JobsMenu(model: model)
            Divider()
            if let current = model.status {
                if let job = current.running {
                    Text("Running: \(job.project) · \(job.task.isEmpty ? job.id : job.task)")
                        .accessibilityLabel("Host job running: \(job.project), \(job.task.isEmpty ? job.id : job.task)")
                    if current.returnJob == nil {
                        Button("I'm back (start grace period)") { model.action("return", job: job.id) }
                    }
                } else {
                    Text("Host lane: \(current.paused ? "Paused" : "Waiting")")
                }
                Text("Host queue: \(current.queued)")
                if !current.waitingReason.isEmpty { Text(current.waitingReason) }
                if let id = current.countdownJob {
                    Text("Starting in \(current.countdownSeconds ?? 0) seconds")
                    Button("Not now (30 minutes)") { model.action("not-now", job: id) }
                } else if let id = current.notNowJob {
                    Button("Not now (30 minutes)") { model.action("not-now", job: id) }
                }
                if let id = current.returnJob {
                    Text("Desktop returned · \(current.graceSeconds ?? 0) seconds left")
                    Button("Take back now") { model.action("take-back", job: id) }
                }
                Divider()
                if current.paused {
                    Button("Resume host jobs") { model.action("resume") }
                } else {
                    Button("Pause host jobs") { model.action("pause") }
                }
            }
            if let error = model.actionError {
                Divider()
                Text(error)
                    .foregroundStyle(.red)
                    .accessibilityLabel("Action failed: \(error)")
                Button("Dismiss error") { model.dismissActionError() }
            }
            Divider()
            Button("Restart Daemon") { model.restart() }
                .disabled(model.daemon.busy || model.controlling)
            Button("Quit Offscreen") { NSApplication.shared.terminate(nil) }
        } label: {
            let state = AppState.model.iconState
            Image(nsImage: MenuBarIcon.image(running: state != .idle, attention: state == .attention))
                .accessibilityLabel(state.label)
        }
    }
}
