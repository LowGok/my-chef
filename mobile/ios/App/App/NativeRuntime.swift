import Foundation
import UIKit
import Network
import UserNotifications

// Process-only handoff. Do not forward account URLs to Capacitor's delegate
// proxies: those retain a lastURL and broadcast the unredacted proof.
final class NativeRuntime {
    static let shared = NativeRuntime()
    static let changed = Notification.Name("MyChefRuntimeChanged")
    private var link: String?
    private var received = -Double.infinity
    private var reminder: String?
    private var reminderReceived = -Double.infinity
    private var lastNotificationTap: (reference: String, time: TimeInterval)?
    private var reminderEnabled = false
    private var reminderToken: String?
    private var online = true
    private let network = NWPathMonitor()
    private var observers: [NSObjectProtocol] = []
    private init() {
        network.pathUpdateHandler = { [weak self] path in
            DispatchQueue.main.async { self?.online = path.status == .satisfied; self?.signal() }
        }
        network.start(queue: DispatchQueue(label: "com.gkldevelopment.mychef.connectivity"))
        for name in [UIApplication.didBecomeActiveNotification, UIApplication.willResignActiveNotification] {
            observers.append(NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in self?.signal() })
        }
    }
    private func signal() { NotificationCenter.default.post(name: Self.changed, object: nil) }
    func receive(_ url: URL) {
        let value = url.absoluteString
        link = value.count <= 512 ? value : nil; received = ProcessInfo.processInfo.systemUptime; signal()
    }
    func receiveReminder(_ reference: String) {
        guard reference.range(of: "^[a-f0-9]{32}$", options: .regularExpression) != nil else { return }
        reminder = reference
        reminderReceived = ProcessInfo.processInfo.systemUptime
        signal()
    }

    func receiveReminderResponse(_ response: UNNotificationResponse?) {
        guard let response, response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let reference = response.notification.request.content.userInfo["reminder"] as? String,
              reference.range(of: "^[a-f0-9]{32}$", options: .regularExpression) != nil else { return }
        let now = ProcessInfo.processInfo.systemUptime
        // UIKit can report one launch to both delegates.
        if let last = lastNotificationTap, last.reference == reference, now - last.time < 1 { return }
        lastNotificationTap = (reference, now)
        receiveReminder(reference)
    }

    #if targetEnvironment(simulator)
    func startFixtureReminderRegistration() {
        guard !reminderEnabled else { return }
        reminderEnabled = true
        reminderToken = String(repeating: "a", count: 64)
        signal()
    }
    #endif

    func startReminderRegistration() {
        if !reminderEnabled {
            reminderEnabled = true
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    func stopReminderRegistration() {
        reminderEnabled = false
        reminderToken = nil
        UIApplication.shared.unregisterForRemoteNotifications()
    }

    func reminderRegistered(_ data: Data) {
        guard reminderEnabled, (16...256).contains(data.count) else { return }
        reminderToken = data.map { String(format: "%02x", $0) }.joined()
        signal()
    }

    func reminderRegistrationFailed() {
        guard reminderEnabled else { return }
        reminderToken = nil
        signal()
    }

    func currentReminderToken() -> String? {
        reminderEnabled ? reminderToken : nil
    }

    func snapshot(origin: String) -> [String:Any] {
        var value: [String:Any] = ["active": UIApplication.shared.applicationState == .active, "online": online, "back": false]
        if let link = link, (0...300).contains(ProcessInfo.processInfo.systemUptime - received) {
            if let url = URLComponents(string: link), url.query == nil,
               let fragment = url.fragment, fragment.range(of: "^[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil,
               ["/app/signup", "/app/verify-email", "/app/change-email", "/app/reset-password"].contains(url.percentEncodedPath),
               link == origin + url.percentEncodedPath + "#" + fragment {
                value["link"] = link
            } else { value["invalidLink"] = true }
        }
        if let reminder, (0...300).contains(ProcessInfo.processInfo.systemUptime - reminderReceived) {
            value["reminder"] = reminder
        }
        reminder = nil
        link = nil
        return value
    }
}
