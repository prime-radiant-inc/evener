# Use Evener on an iPhone

The native app connects your iPhone to an Evener hub. The agent still runs on the hub's selected host; the phone lets you start sessions, read conversations, and send instructions. It does not run the coding agent locally on the phone.

**Distribution limitation:** this guide documents the source-build path and the repository's internal TestFlight tooling. It does not promise a public App Store listing or an open TestFlight invitation. The current delivery scope is iPhone. iPad work is paused, and Android delivery is deferred despite retained Android sources.

**Tutorial verification is deferred:** the executed tutorial matrix covers CLI
and web only. These iPhone instructions, simulator interaction, physical-device
behavior, and camera pairing have not been verified by those tours. Browser
screenshots are not native evidence. See the [coverage limits](developing-evener/tutorial-tours.md#active-coverage).

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

Keep the authenticated browser open on the computer. There are two pairing flows. The default QR code contains a reusable sign-in link. Choosing another hub address creates a short-lived invitation that requires browser approval instead.

### Scan the default code

1. On the computer, open **Settings → Devices** in the browser. Check that the hub address is reachable from the phone.
2. On the iPhone, choose **Scan pairing code**, grant camera access, and scan the browser's code.
3. On the iPhone, review the hub address and name, then choose **Connect**. This direct-link flow has no separate approval step in the browser.

Keep the code and **Copy pairing link** output private. They contain access to the hub and remain usable until its authentication token is rotated. On an untrusted network, use encrypted transport before displaying or transferring them.

### Use another hub address

If the browser offers **Use another Hub address** or **Enter Hub address**, use that control to supply an address the phone can reach. Review the connection choice and select **Create invitation** to display a QR invitation. This does not change the hub's listener or network configuration.

1. Scan the invitation with **Scan pairing code** on the iPhone.
2. The iPhone shows **Compare code** and waits. On the computer, the browser shows the device requesting access and its comparison code.
3. Compare the full code on both screens. If they match, use that code's approval button in the computer's browser. If they differ, do not approve; cancel pairing on the phone and start again with a new invitation.
4. After approval, review the hub address and name on the iPhone, then choose **Connect**.

Invitations expire after ten minutes. If the phone reports expiry or that another device was approved, create a new invitation and scan it again. A device name alone is not proof that the request came from your phone.

### Verify the connection or enter it manually

Open a known session and confirm that its recent conversation appears. Send a harmless follow-up to verify control as well as reading.

The app also offers **Paste pairing link** and **Enter the address**. Manual entry needs the hub's base address, such as `https://hub.example.invalid:9180` (a schematic example, not a working server). Include the `http://` or `https://` scheme and any required port, but no session path. This base address is also called the hub's **origin**.

Manual entry also needs the hub's **bearer token**, a secret that grants access to the hub. It is not a provider API key. The hub operator can obtain it from the `auth-token` file in the hub state root, `${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth-token` by default. A `hub_state_root` override in `hub.toml` relocates it. Transfer it privately, and keep it out of screenshots, prompts, and source control. The app stores credentials in the phone's secure credential storage, SecureStore.

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

1. On the computer, open the hub in the browser. If it fails there too, inspect the terminal or service running `evener hub` before changing the phone's setup.
2. On the phone, connect to the intended Wi-Fi or VPN. Compare the saved address with the reachable address shown in **Settings → Devices**. Remove any session path, and do not use `127.0.0.1` for another computer.
3. If the phone reports that an invitation expired or another device was approved, create a new invitation. The default reusable pairing link has no ten-minute expiry; if the hub token was rotated, obtain a new link instead of repeatedly scanning the old one.
4. If the app reports a protocol or version incompatibility, update through the app's distribution channel and the hub's supported update path. Run `evener --version` on the hub host and retain the app build identifier supplied by its distributor for a support report. Do not assume a connectivity error alone proves a version mismatch.

Use [troubleshooting](troubleshooting.md) for server-side diagnosis. Report versions, the action, and the visible error, but redact tokens and pairing links.
