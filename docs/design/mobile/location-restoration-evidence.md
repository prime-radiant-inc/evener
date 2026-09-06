# Native location restoration evidence

Source `dc2dd032f`, 5 September 2026. Both release builds succeeded and installed: iPhone 17 Pro / iOS 26.5 and Pixel 7 / Android 35. Android APK SHA-256: `6682ba1fb2fa8be80f62abce22c5851300543ed4bbead9060c6bca077ab8bc47`. Native tests: 53 passed in eleven files. TypeScript, targeted Biome and diff checks passed. No whole-repository merge gate was run.

## Runtime observations

- iOS opened Reading re / Mobile playground. Rebuilding and launching the final app restored that conversation directly. A separate stop/launch repeated the result.
- iOS Sessions back action returned to the restored roster; Hubs returned to saved hubs. Stopping and launching there stayed on Hubs, confirming deliberate clearing.
- Android opened Reading reference / Mobile playground. Changing font scale from 1.0 to 1.5 recreated the activity and automatically reopened the same hub and conversation at the larger size.
- Android typed `Restore this conversation and draft`, force-stopped, and launched. Both destination and exact unsent draft reappeared. No send action was invoked.
- Android toolbar Navigate up returned to Sessions; Hubs returned to saved hubs. Selected Playground / Mobile playground (the same `demo:playground` reference on a different hub), force-stopped and relaunched. The app showed Playground's distinct transcript and empty composer. The Reading reference draft remained isolated.
- Android system Back via `KEYCODE_BACK` exited to the launcher from the restored conversation, whereas toolbar Navigate up popped to Sessions. System/predictive Back integration needs investigation; the manifest enables `android:enableOnBackInvokedCallback`. This is not accepted back behavior.

[Android restored conversation and draft](assets/location/android-restored.png) and [iOS restored conversation](assets/location/ios-restored.png) are inspected native screenshots. Tests use the scripted hubs on ports 9196/9198; no production sessions were mutated.

## Review and limits

Independent review found that writing the initial Hubs state after a transient startup read failure could erase an unread bookmark. The final implementation performs no initial-mount write; navigation changes start persistence. Review confirmed the fix. Unit tests cover exact saved hub identity, removed hub rejection, malformed data, back-stack construction, deliberate clearing and storage errors. Native tests establish SQLite persistence under actual app restart, but do not inject disk failures or prove process death at every instruction boundary.

The saved bookmark contains only hub ID and optional conversation reference/title. Drafts use their existing separate repository. Session-creation forms restore to Sessions rather than replaying creation. Reading offset, form drafts, deleted-session handling, representative physical devices and complete system Back behavior remain outstanding. The reading fixture retains the intentional unsent Android draft for continued testing.

## Android system Back correction

The app had opted into predictive Back while using `@react-navigation/native-stack` 7.18.10 and react-native-screens 4.26.0. On the API 35 emulator, Back exited the activity despite a valid navigator stack. Installed React Native's dispatcher callback is registered only when both runtime and target SDK are at least 36; our target is 36 but runtime is 35. React Navigation's normal handler correctly checks `canGoBack()` and dispatches `goBack()` when it receives the event.

Set Expo `android.predictiveBackGestureEnabled` to false and regenerated the Android project. The generated manifest now uses `android:enableOnBackInvokedCallback="false"`. This corrects the configuration for the current navigator; it does not claim predictive transition previews. [Maintainer discussion](https://github.com/software-mansion/react-native-screens/discussions/2540) describes the v4 predictive-navigation limitation; [Expo 57 experimental-stack docs](https://docs.expo.dev/versions/v57.0.0/sdk/router/experimental-stack/) identify predictive support in the newer experimental stack. Adopting that stack requires a separate navigation migration and regression pass.

The corrected release build succeeded and installed. APK SHA-256: `ad65e69359bfad93b2c14bc36d3f243f8a2d20a132d03fb355af0de6829805ea`. Native observations on Android 15/API 35:

- Launch restored Playground / Mobile playground. System Back (`KEYCODE_BACK`) returned to Sessions, then Hubs; a third Back returned to the launcher.
- Reopened from the launcher, selected the conversation, and focused Message. Input-method state reported `mInputShown=true`. Back changed it to false while leaving the conversation and full-height composer visible; a second Back returned to Sessions.
- Reopened the conversation and performed a left-edge swipe from x=1 to x=400. It returned to Sessions.

No JavaScript behavior or iOS configuration changed. The existing 53-test result predates this configuration-only correction; acceptance evidence here is the regenerated manifest, successful release build and native interaction checks. Android 16+, physical devices, predictive transition previews and the remaining full mobile feature scope are not certified.
