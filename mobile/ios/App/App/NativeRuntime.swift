import Foundation
import UIKit
import Network

// Process-only handoff. Do not forward account URLs to Capacitor's delegate
// proxies: those retain a lastURL and broadcast the unredacted proof.
final class NativeRuntime {
    static let shared = NativeRuntime()
    static let changed = Notification.Name("MyChefRuntimeChanged")
    private var link: String?
    private var received = -Double.infinity
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
        link = nil; return value
    }
}
