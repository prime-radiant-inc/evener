// The single React surface outlives a scene's window. UIKit may disconnect a
// background scene without ending the process or its navigation and drafts.
class SceneDelegate: UIResponder, UIWindowSceneDelegate {
  var window: UIWindow?

  private var appDelegate: AppDelegate {
    UIApplication.shared.delegate as! AppDelegate
  }

  func scene(
    _ scene: UIScene,
    willConnectTo session: UISceneSession,
    options connectionOptions: UIScene.ConnectionOptions
  ) {
    guard let windowScene = scene as? UIWindowScene else { return }
    let window = UIWindow(windowScene: windowScene)
    self.window = window
    appDelegate.window = window

    if let root = appDelegate.sceneRootViewController {
      window.rootViewController = root
      window.makeKeyAndVisible()
      open(connectionOptions.urlContexts, cold: false)
      for activity in connectionOptions.userActivities {
        self.scene(scene, continue: activity)
      }
    } else {
      var launchOptions = appDelegate.sceneLaunchOptions ?? [:]
      if let context = connectionOptions.urlContexts.first {
        launchOptions[.url] = context.url
        launchOptions[.sourceApplication] = context.options.sourceApplication
      }
      if let activity = connectionOptions.userActivities.first(where: {
        $0.activityType == NSUserActivityTypeBrowsingWeb && $0.webpageURL != nil
      }) {
        // RCTLinkingManager reads this dictionary for Linking.getInitialURL().
        launchOptions[.userActivityDictionary] = [
          UIApplication.LaunchOptionsKey.userActivityType.rawValue: activity.activityType,
          "UIApplicationLaunchOptionsUserActivityKey": activity
        ]
      }
      appDelegate.reactNativeFactory?.startReactNative(
        withModuleName: "main", in: window, launchOptions: launchOptions)
      appDelegate.sceneRootViewController = window.rootViewController
      appDelegate.sceneLaunchOptions = nil
      // RN consumes cold links from launchOptions. Only Expo needs callbacks
      // here, otherwise the same link can also arrive as a second JS event.
      open(connectionOptions.urlContexts, cold: true)
      for activity in connectionOptions.userActivities {
        appDelegate.deliverColdSceneActivity(activity)
      }
    }
  }

  func sceneDidDisconnect(_ scene: UIScene) {
    if appDelegate.window === window { appDelegate.window = nil }
    window?.rootViewController = nil
    window = nil
  }

  func scene(_ scene: UIScene, openURLContexts URLContexts: Set<UIOpenURLContext>) {
    open(URLContexts, cold: false)
  }

  private func open(_ contexts: Set<UIOpenURLContext>, cold: Bool) {
    for context in contexts {
      var options: [UIApplication.OpenURLOptionsKey: Any] = [
        .openInPlace: context.options.openInPlace
      ]
      options[.sourceApplication] = context.options.sourceApplication
      options[.annotation] = context.options.annotation
      if cold {
        appDelegate.deliverColdSceneURL(context.url, options: options)
      } else {
        _ = appDelegate.application(UIApplication.shared, open: context.url, options: options)
      }
    }
  }

  func scene(_ scene: UIScene, continue userActivity: NSUserActivity) {
    _ = appDelegate.application(UIApplication.shared, continue: userActivity, restorationHandler: { _ in })
  }

  // UIKit still posts UIApplication notifications for RN AppState. Forward
  // delegate methods only, so Expo subscribers run without duplicate events.
  func sceneDidBecomeActive(_ scene: UIScene) {
    appDelegate.applicationDidBecomeActive(UIApplication.shared)
  }

  func sceneWillResignActive(_ scene: UIScene) {
    appDelegate.applicationWillResignActive(UIApplication.shared)
  }

  func sceneWillEnterForeground(_ scene: UIScene) {
    appDelegate.applicationWillEnterForeground(UIApplication.shared)
  }

  func sceneDidEnterBackground(_ scene: UIScene) {
    appDelegate.applicationDidEnterBackground(UIApplication.shared)
  }
}

extension AppDelegate {
  func deliverColdSceneURL(_ url: URL, options: [UIApplication.OpenURLOptionsKey: Any]) {
    _ = super.application(UIApplication.shared, open: url, options: options)
  }

  func deliverColdSceneActivity(_ activity: NSUserActivity) {
    _ = super.application(UIApplication.shared, continue: activity, restorationHandler: { _ in })
  }
}
