import AppKit

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
