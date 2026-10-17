# Use Evener on an iPhone

The native app connects your iPhone to an Evener hub. The agent still runs on the hub's selected host; the phone lets you start sessions, read conversations, and send instructions. It does not run the coding agent locally on the phone.

**Distribution limitation:** this guide documents the source-build path and the repository's internal TestFlight tooling. It does not promise a public App Store listing or an open TestFlight invitation. The current delivery scope is iPhone. iPad work is paused, and Android delivery is deferred despite retained Android sources.

## Obtain a build

If your organization supplies a signed build or a TestFlight invitation, use that distribution channel and confirm which source version it contains. A hub snapshot and an older phone build may not implement the same protocol.

Otherwise, build from source on a Mac. The [iOS build and distribution guide](design/mobile/ios-build-distribution.md) owns the exact prerequisites and locked build procedure, including Node, Xcode, Ruby, Bundler, and CocoaPods versions. It covers dependency installation, native-project generation, and the `Evener` scheme in `mobile-native/ios/Evener.xcworkspace`.

Use a standalone development or Release build. Expo Go is not the supported path for the current native dependencies. Simulator builds do not require a paid provisioning profile; physical-device signing and distribution have their own Apple requirements. Do not disable simulator code signing: secure credential storage needs the app entitlement.

The [native contributor README](../mobile-native/README.md) describes development commands and current qualification limits. Automated source checks do not prove that a particular distributed build installs or behaves correctly on your phone.

## Prepare a reachable hub

First complete [browser setup and provider configuration](getting-started.md). Verify that the hub works from its own computer before adding a phone.

A physical iPhone cannot reach your computer through `127.0.0.1`: that address means the phone itself. Give it a hub address reachable over your LAN or VPN. The default loopback-only hub listener must be configured appropriately; follow the [hub runbook](evener-hub.md) and [remote operations guide](evener-hub-remote-operations.md).

Keep authentication enabled. Use HTTPS where transport encryption is needed, or a suitably protected private network. The app permits user-entered HTTP hubs; that permission does not make plaintext transport safe on an untrusted network. Do not expose the hub publicly just to make a pairing code work.

## Pair the phone

Keep the authenticated browser open on the computer:

1. Open **Settings → Mobile app** in the browser and display a pairing code. Check the destination address before using it; it must be reachable from the phone.
2. In the native app, choose **Scan pairing code**. Grant camera access and scan the browser's code.
3. Follow the approval screen. For an invitation, compare the displayed comparison value on both devices and approve only the claim from your phone in the browser.
4. Review the hub address and name on the phone, then choose **Connect**.
5. Open a known session and confirm that its recent conversation appears. Send a harmless follow-up to verify control as well as reading.

The app also offers **Paste pairing link** and **Enter the address**. Manual entry needs the hub's HTTP(S) origin and bearer token, not a provider API key. Use those alternatives only with credentials obtained through a trusted path. Keep tokens out of screenshots, prompts, and source control. The app stores credentials in SecureStore.

Pairing grants access to the hub. It does not configure model credentials, make an unreachable address reachable, or start a stopped hub. The [device-pairing reference](device-pairing.md) defines invitation expiry, approval, and credential delivery for client authors.

## Work from the phone

Choose the hub, open a session, and use its composer to continue the conversation. New work runs in a directory on the selected host. Review that host and directory before submitting a task; the phone's storage is not the project's working directory.

The [daily-work guide](daily-work.md) explains the distinction between sending follow-up input, steering current work, and stopping it. Native controls need not occupy the same place as browser controls. Inspect the session menu when an action is not beside the composer.

Backgrounding the app is not a stop request to the agent. On returning, let it reconnect and recover the session before sending the same request again. A disconnected phone cannot tell you that an operation failed merely because it did not receive the reply.

For a code change, use the phone to inspect progress and ask questions, then review the actual diff and run the acceptance checks on the project host. A readable transcript is not proof that the change has been merged or deployed.

## Update and recover

Update the app through the channel that supplied it. For a source build, rebuild and reinstall from the desired source revision using the locked build procedure. Internal TestFlight tooling requires distribution credentials and an available build; its presence in the repository is not a public update service.

Updating hub binaries does not update the phone app. Nor does a successful upload prove a physical-device update preserved app data. Keep a trusted way to reach the hub from the browser in case the phone needs to reconnect or pair again.

If connection fails, check these in order:

- Is the hub running, and can the computer's browser open it?
- Is the phone on the intended network or VPN, and is the destination reachable?
- Is the address a hub origin rather than a session URL or the phone's loopback address?
- Has the invitation expired, or does the token need replacement?
- Are the app and hub versions compatible?

Use [troubleshooting](troubleshooting.md) for server-side diagnosis. Report versions, the action, and the visible error, but redact tokens and pairing links.
