import AppKit

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
        return NSRect(
            x: visibleFrame.midX - width / 2,
            y: max(visibleFrame.minY, visibleFrame.maxY - height - 16),
            width: width, height: height)
    }
}

@MainActor final class CountdownPanel {
    let jobID: String
    private let panel: NonactivatingCountdownPanel
    private let label: NSTextField
    private let detail: NSTextField
    private let action: CountdownAction

    init(jobID: String, onNotNow: @escaping (String) -> Void) {
        self.jobID = jobID
        action = CountdownAction(jobID: jobID, onNotNow: onNotNow)
        panel = NonactivatingCountdownPanel(
            contentRect: NSRect(x: 0, y: 0, width: 300, height: 122),
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
        let button = CountdownButton(
            title: "Not now (30 minutes)", target: action,
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
                stack.bottomAnchor.constraint(lessThanOrEqualTo: content.bottomAnchor, constant: -12),
            ])
        }
    }

    func show(seconds: Int?, error: String?) {
        label.stringValue = seconds.map { "Host job starts in \($0) seconds" } ?? "Host job waiting for idle desktop"
        detail.stringValue = error ?? "Use Not now to pause host jobs before they take the screen."
        if !panel.isVisible {
            let screen =
                NSScreen.screens.first { $0.frame.contains(NSEvent.mouseLocation) }
                ?? NSScreen.main ?? NSScreen.screens.first
            if let screen {
                panel.layoutIfNeeded()
                panel.setFrame(
                    CountdownPlacement.frame(
                        panelSize: panel.frame.size,
                        visibleFrame: screen.visibleFrame), display: true)
            }
            panel.orderFrontRegardless()  // no activation or key-window request
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
