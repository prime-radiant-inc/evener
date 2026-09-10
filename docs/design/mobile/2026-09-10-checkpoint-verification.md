# Native checkpoint verification, September 10

This receipt describes a Release iPhone simulator journey against an isolated, directly authenticated Evener hub. The provider alone was scripted at the LLM boundary. Native UI actions drove the session; an independently installed AppWire package read back the authoritative transcript. No production hub or session was mutated.

## Artifact identity

- Native source: `b246b53f6069bc4c20475a48bc6451543650d280`.
- Native executable SHA-256: `9fa24709c3762ee178c85fe8df4e298636037b1b5cf2e86571f9aa0b46b0f479`.
- JavaScript bundle SHA-256: `072a49907b9bab22a6711f32c0c8dafd56fe37529ed333cdfeabda6e4f4b2d45`.
- Backend source: `f694911a2521e12923d25c8bc8eeb8a76554ba52`; executable SHA-256 `03675d083688588f5843f4ca10873266c1ee289b4af37372d521f1e9c45a736b` and worker SHA-256 `f67187d518815e8c48ef0c6c2df8354c5773fec7977ca6606857de604b1fb6c9`.
- Backend version reports `f694911a2-dirty`; the captured checkout was clean before and after the build. The unexplained build marker remains recorded rather than claiming a clean-version artifact.
- Xcode 26.6 (17F113), Node 22.13.1, iPhone 17 Pro simulator running iOS 26.5. The Release app has device family `[1]` and the expected simulated application identifier.

## Observed behavior

1. Installed over the existing app after backing up its application and data container. The existing unsent draft was visible after the update.
2. Saved a new isolated hub profile and credential, connected over AppWire v5, and created a session from the native app. The session appears under its workspace project.
3. Sent an ordinary prompt. Native Markdown, a link and a code block rendered; authoritative reasoning and response items completed.
4. Started a held provider turn and tapped Stop. The provider observed client cancellation; readback showed the turn interrupted and the hub idle. A subsequent native send completed.
5. Opened a pending question, entered a note, closed and reopened the sheet, and submitted the retained answer. Readback contained the selected answer and exact note once, followed by a completed response.
6. Cold-launched the app. It reconnected to the selected hub, restored the conversation and retained the unsent draft.
7. Denied an outside-workspace write through the native approval sheet. The target remained absent, the tool reported failure, and the session resumed. A separate composer draft remained intact.
8. Allowed a separate outside-workspace write once. The owned temporary file contained the expected contents, and the session resumed.

Both write targets were inside the isolated fixture directory. Every original persisted draft row was preserved: 13 ordinary drafts, 11 question drafts, one creation draft and three Question Sheet positions. All nine original conversation reader-position entries were unchanged. The app-data backup did not include a historical keychain snapshot, so this comparison does not prove byte-for-byte preservation of every saved credential.

## Limits and integration checks

The native candidate subsequently incorporated activity paging review fixes and package/CI qualification fixes. Its local native gate passes 718 native tests, 673 shared-session tests and strict native TypeScript checks. The package gate runs the packed example through real sockets from an outside-checkout installation, including ESM/CommonJS declarations and exports.

This journey is evidence for the artifact identities above. It does not qualify a later merged artifact, representative automatic-paging performance, physical iPhone update behavior, all provider/model pickers, image drafts, or the complete iPhone functionality matrix. A fresh reviewed TestFlight build and the remaining [acceptance gates](acceptance.md) are still required. iPad and dedicated accessibility work remain paused.
