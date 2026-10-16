import UIKit
import Mobile

// The whole application is one view controller from the xcframework that
// `ebitenmobile bind` produced from the Go package in the parent directory;
// the game it runs is set there (mobile.SetGame in mobile.go).
@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?
    private let game = MobileEbitenViewController()
    private var host: ReportHost?

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.backgroundColor = .black
        window.rootViewController = game
        window.makeKeyAndVisible()
        self.window = window

        // The check mode's Share and Open issue buttons, and the model for
        // its report, which Go cannot read.
        let host = ReportHost(presenter: game)
        self.host = host
        MobileSetHost(host)
        MobileSetDeviceInfo(ReportHost.deviceInfo())
        // The check runs for a minute without a touch; a locked screen
        // would take the hardware codecs away in the middle of it.
        application.isIdleTimerDisabled = true
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

// ReportHost is the Go check's Host (check.go): the share sheet for the
// Markdown report and the browser for the prefilled GitHub issue. Go calls
// it from its own thread.
final class ReportHost: NSObject, MobileHostProtocol {
    private weak var presenter: UIViewController?

    init(presenter: UIViewController) {
        self.presenter = presenter
    }

    func share(_ subject: String?, text: String?) {
        let subject = subject ?? "", text = text ?? ""
        DispatchQueue.main.async {
            guard let presenter = self.presenter else { return }
            let sheet = UIActivityViewController(activityItems: [ReportItem(subject: subject, text: text)],
                                                 applicationActivities: nil)
            // On an iPad the sheet is a popover and needs an anchor.
            if let popover = sheet.popoverPresentationController {
                popover.sourceView = presenter.view
                popover.sourceRect = CGRect(x: presenter.view.bounds.midX, y: presenter.view.bounds.maxY - 80,
                                            width: 1, height: 1)
            }
            presenter.present(sheet, animated: true)
        }
    }

    func openURL(_ url: String?) {
        guard let url = url.flatMap(URL.init(string:)) else { return }
        DispatchQueue.main.async {
            UIApplication.shared.open(url)
        }
    }

    // deviceInfo is the model identifier (such as iPhone16,1) and the OS
    // version; the Simulator reports the model it simulates.
    static func deviceInfo() -> String {
        var model = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"].map { "\($0) Simulator" }
        if model == nil {
            var info = utsname()
            uname(&info)
            model = withUnsafeBytes(of: &info.machine) { raw in
                String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
            }
        }
        let device = UIDevice.current
        return "\(model ?? device.model), \(device.systemName) \(device.systemVersion)"
    }
}

// ReportItem gives the share sheet the report as text and the issue title
// as the subject, for Mail and the like.
final class ReportItem: NSObject, UIActivityItemSource {
    private let subject: String
    private let text: String

    init(subject: String, text: String) {
        self.subject = subject
        self.text = text
    }

    func activityViewControllerPlaceholderItem(_ activityViewController: UIActivityViewController) -> Any {
        text
    }

    func activityViewController(_ activityViewController: UIActivityViewController,
                                itemForActivityType activityType: UIActivity.ActivityType?) -> Any? {
        text
    }

    func activityViewController(_ activityViewController: UIActivityViewController,
                                subjectForActivityType activityType: UIActivity.ActivityType?) -> String {
        subject
    }
}
