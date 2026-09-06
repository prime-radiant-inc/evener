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
