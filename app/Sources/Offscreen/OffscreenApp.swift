import AppKit
import CoreGraphics
import Foundation
import SwiftUI
import UserNotifications

@MainActor
@Observable final class HostModel {
    var status: HostStatus?
    var connectionError: String?
    var actionError: String?
    private var countdownPanel: CountdownPanel?
    private let bridge = Bridge(socketPath: Bridge.defaultSocket())
    private var timer: Timer?
    private var eventTap: CFMachPort?
    private var tapSource: CFRunLoopSource?
    // Input Monitoring, the event tap and snapshots run only while the daemon
    // has host work, so VM-only users are never asked for the permission.
    private var hostActive = false
    private var sensing = false
    private var previousNotice = ""
    private var refreshing = false
    private var refreshAfterInput = false
    private var pendingReturn = ""
    private let probe = CommandLine.arguments.contains("--probe-input")
    private let manualReturn = UserDefaults.standard.bool(forKey: "ManualReturn")
    private let verifiedFilter = UserDefaults.standard.bool(forKey: "VerifiedInputFilter")

    func start() {
        guard timer == nil else { return }
        let heartbeat = Timer(timeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refresh() }
        }
        RunLoop.main.add(heartbeat, forMode: .common)
        timer = heartbeat
        refresh()
    }

    func retry() { refresh() }

    private func refresh() {
        guard !refreshing else { return }
        refreshing = true
        let sense = HostSensing.needed(hostActive: hostActive, probe: probe)
        setSensing(sense)
        let tapReady = eventTap.map { CFMachPortIsValid($0) && CGEvent.tapIsEnabled(tap: $0) } ?? false
        let bridge = self.bridge
        Task.detached {
            do {
                let state = sense
                    ? try bridge.request("/host/snapshot", snapshot: Sensors.snapshot(eventTapReady: tapReady))
                    : try bridge.request("/host/status")
                await MainActor.run {
                    self.refreshing = false
                    self.hostActive = HostSensing.hostActive(state)
                    self.update(state)
                    // Send the first snapshot now rather than on the next
                    // heartbeat so admission isn't delayed by the switch.
                    if self.refreshAfterInput || HostSensing.needed(hostActive: self.hostActive, probe: self.probe) != sense {
                        self.refreshAfterInput = false
                        self.refresh()
                    }
                }
            } catch {
                await MainActor.run {
                    self.refreshing = false
                    self.status = nil
                    self.countdownPanel?.hide()
                    self.connectionError = "Daemon unavailable. Start it with offscreen daemon install."
                    self.refreshAfterInput = false
                }
            }
        }
    }

    private func update(_ new: HostStatus) {
        status = new
        connectionError = nil
        updateCountdownPanel(new)
        if let id = new.returnJob, id != previousNotice {
            previousNotice = id
            let notification = UNMutableNotificationContent()
            let name = new.running.map { "\($0.project) · \($0.task.isEmpty ? $0.id : $0.task)" } ?? id
            notification.title = "Host job is using your desktop"
            notification.body = "\(name) continues for \(new.graceSeconds ?? 0)s. Take back now to stop and requeue it."
            notification.categoryIdentifier = "HOST_RETURN"
            let request = UNNotificationRequest(identifier: "return-\(id)", content: notification, trigger: nil)
            // Ask for Notifications only when there is one to show. After the
            // first answer macOS returns it without prompting again.
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, _ in
                if granted { UNUserNotificationCenter.current().add(request) }
            }
        }
        if new.returnJob == nil { previousNotice = "" }
        if pendingReturn != new.running?.id || new.returnJob != nil { pendingReturn = "" }
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
        if let status { updateCountdownPanel(status) }
    }

    func action(_ name: String, job: String = "") {
        let bridge = self.bridge
        Task.detached {
            do {
                let state = try bridge.request("/host/\(name)" + (job.isEmpty ? "" : "/\(job)"))
                await MainActor.run {
                    self.actionError = nil
                    self.update(state)
                }
            } catch {
                await MainActor.run {
                    if name == "return", self.pendingReturn == job { self.pendingReturn = "" }
                    self.actionError = "\(name == "not-now" ? "Not now" : name == "take-back" ? "Take back" : "Host action") failed: \(error)"
                    if let status = self.status { self.updateCountdownPanel(status) }
                }
            }
        }
    }

    private func setSensing(_ on: Bool) {
        guard on else {
            if sensing { removeTap() }
            sensing = false
            return
        }
        if !sensing {
            sensing = true
            _ = CGRequestListenEventAccess()
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
        if type == .leftMouseDown, status?.notNowJob != nil {
            if refreshing { refreshAfterInput = true } else { refresh() }
        }
        let pid = event.getIntegerValueField(.eventSourceUnixProcessID)
        let state = event.getIntegerValueField(.eventSourceStateID)
        if probe { print("type=\(type.rawValue) pid=\(pid) sourceState=\(state)"); fflush(stdout) }
        // Candidate only: physical-vs-injected behavior requires the human probe.
        // Never consider injected events (which reset HIDIdleTime) a return.
        let running: String? = ["preparing", "running", "collecting"].contains(status?.running?.state ?? "")
            ? status?.running?.id : nil
        let returning = status?.returnJob != nil || pendingReturn == running
        guard let id = InputFilter.candidatePhysical(pid: pid, runningJob: running,
                                                     alreadyReturning: returning,
                                                     manualReturn: manualReturn,
                                                     verified: verifiedFilter) else { return }
        pendingReturn = id
        action("return", job: id)
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
        let id = String(response.notification.request.identifier.dropFirst("return-".count))
        if response.actionIdentifier == "TAKE_BACK" {
            Task { @MainActor in AppState.model.action("take-back", job: id) }
        }
        completionHandler()
    }
}

@MainActor enum AppState { static let model = HostModel() }

@main struct OffscreenApp: App {
    init() {
        let action = UNNotificationAction(identifier: "TAKE_BACK", title: "Take back now", options: [])
        let category = UNNotificationCategory(identifier: "HOST_RETURN", actions: [action], intentIdentifiers: [])
        UNUserNotificationCenter.current().setNotificationCategories([category])
        UNUserNotificationCenter.current().delegate = NotificationHandler.shared
        Task { @MainActor in AppState.model.start() }
    }

    var body: some Scene {
        MenuBarExtra {
            let model = AppState.model
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
            } else {
                Text(model.connectionError ?? "Checking host lane…")
                Button("Retry") { model.retry() }
            }
            if let error = model.actionError {
                Divider()
                Text(error)
                    .foregroundStyle(.red)
                    .accessibilityLabel("Host action failed: \(error)")
                Button("Dismiss error") { model.dismissActionError() }
            }
            Divider()
            Button("Quit Offscreen") { NSApplication.shared.terminate(nil) }
        } label: {
            Image(nsImage: MenuBarIcon.image(running: AppState.model.status?.running != nil))
                .accessibilityLabel("Offscreen")
        }
    }
}
