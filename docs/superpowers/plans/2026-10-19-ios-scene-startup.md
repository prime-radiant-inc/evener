# iOS scene startup integration

The installed SDK rejects the generated application before it displays a window. The first hypothesis, missing scene lifecycle adoption, is confirmed by UIKit's launch rejection and the missing `UIApplicationSceneManifest`/scene delegate. The native design changes are not on the failing startup path.

## Scope

Keep Expo 57, React Native 0.86, bundle identity, storage, React navigation and JavaScript recovery owners. Add one application scene through tracked Expo prebuild integration. No multi-window support, new dependencies, toolchain replacement, publication or deployment.

## Implementation and checks

1. Evaluate the real configured Expo mods in a regression test. Require a single scene manifest while preserving identity, URL schemes and unrelated settings. Prove the missing manifest fails before implementation. Keep startup transformations idempotent and reject unknown templates so an SDK upgrade cannot silently lose the integration.
2. Keep factory initialization and Expo launch subscribers in AppDelegate. Start React in a scene-owned window. Retain the root controller for scene reconnection, merge cold links into React launch options, and forward warm links and Expo lifecycle callbacks without synthesizing UIKit notifications.
3. Generate the real iOS project twice and build it with normal simulator signing. Install over the existing bundle without uninstalling or resetting stored data. Demonstrate useful launch, a disappearing splash, native AppState transitions, reconnect and retained navigation/draft state. Check cold/warm links at the React boundary, not only the launch request's exit code.
4. Update the native build guide and subsystem ownership. Run the native gate, independent code review and simplification review. Commit the startup work, then resume the bounded light/dark/large-text visual review.

## Verification boundaries

Plugin tests establish persistent generated configuration, not runtime lifecycle behavior. A build proves compilation, not launch. A simulator launch request may return a PID immediately before UIKit kills the process. Actual native views, AppState and link delivery must be inspected. Universal-link association and live provider authorization require their respective external services; report them separately if not exercised. Home/background is not scene disconnection.
