import UIKit
import Mobile

// The whole application is one view controller from the xcframework that
// `ebitenmobile bind` produced from the Go package in the parent directory;
// the game it runs is set there (mobile.SetGame in mobile.go).
@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?
    private let game = MobileEbitenViewController()

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.backgroundColor = .black
        window.rootViewController = game
        window.makeKeyAndVisible()
        self.window = window
        return true
    }

    // iOS takes the hardware codecs away from an application in the
    // background; the player's VideoToolbox sessions are invalidated and the
    // library opens new ones when decoding resumes. Suspending the game loop
    // meanwhile is what Ebitengine recommends.
    func applicationWillResignActive(_ application: UIApplication) {
        game.suspendGame()
    }

    func applicationDidBecomeActive(_ application: UIApplication) {
        game.resumeGame()
    }
}
