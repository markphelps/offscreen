import Foundation
@preconcurrency import UserNotifications
import os

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
                log.warning(
                    "notification \(request.identifier, privacy: .public) not shown: \(error?.localizedDescription ?? "not allowed", privacy: .public)"
                )
                return
            }
            center.add(request) { error in
                if let error {
                    log.warning(
                        "notification \(request.identifier, privacy: .public) failed: \(error.localizedDescription, privacy: .public)"
                    )
                }
            }
        }
    }
}

final class NotificationHandler: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    static let shared = NotificationHandler()
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping @Sendable (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let identifier = response.notification.request.identifier
        if identifier.hasPrefix("return-"), response.actionIdentifier == "TAKE_BACK" {
            let id = String(identifier.dropFirst("return-".count))
            Task { @MainActor in AppState.model.action("take-back", job: id) }
        } else if identifier.hasPrefix("failed-"), response.actionIdentifier == UNNotificationDefaultActionIdentifier,
            let path = response.notification.request.content.userInfo["artifacts"] as? String
        {
            Task { @MainActor in AppState.model.revealArtifacts(path) }
        }
        completionHandler()
    }
}
