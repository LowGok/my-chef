import UIKit
import Capacitor

class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?

    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        guard let windowScene = scene as? UIWindowScene else { return }

        NativeRuntime.shared.receiveReminderResponse(connectionOptions.notificationResponse)
        window = UIWindow(windowScene: windowScene)
        window?.rootViewController = MyChefBridgeViewController()
        window?.makeKeyAndVisible()

        for context in connectionOptions.urlContexts { NativeRuntime.shared.receive(context.url) }
        for activity in connectionOptions.userActivities {
            if activity.activityType == NSUserActivityTypeBrowsingWeb, let url = activity.webpageURL { NativeRuntime.shared.receive(url) }
        }
    }

    func scene(_ scene: UIScene, openURLContexts URLContexts: Set<UIOpenURLContext>) {
        for context in URLContexts { NativeRuntime.shared.receive(context.url) }
    }

    func scene(_ scene: UIScene, continue userActivity: NSUserActivity) {
        if userActivity.activityType == NSUserActivityTypeBrowsingWeb, let url = userActivity.webpageURL { NativeRuntime.shared.receive(url) }
    }
}
