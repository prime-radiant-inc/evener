# Native mobile first slice

Jesse approved replacing the Tauri presentation layer with React Native and
asked to get something basic running, then iterate. Work starts from the
rebased mobile branch, preserving the old app as a reference.

Create `mobile-native/` with Expo, TypeScript, React Navigation native stack,
and native React Native controls. Expo simplifies running the same code on
both platforms; bare React Native would add native project maintenance before
we need it. Two separate native apps would duplicate the session experience.

The first slice connects to a hub using a manually entered origin and optional
bearer token, saves multiple named hubs with an explicit hub switcher, lists existing sessions, opens a text transcript, and sends or
interrupts a turn according to the server capabilities. It shows loading,
connection, empty, and error states. Tokens use Expo SecureStore and never
appear in URLs or logs. Shared AppWire client, conversation projection,
services, and store are imported from their existing source; no duplicated
wire protocol and no Tauri dependency in the native runtime.

Use native stack navigation for iOS swipe-back and Android back handling,
safe-area insets, keyboard avoidance, native text selection, system text sizes,
and light/dark colors. The transcript is a native virtualized list. Initial
rendering is plain text, including activity disclosure; rich Markdown, session
creation, attachments, pairing QR, and voice are subsequent iterations.

App backgrounding disconnects transport; foregrounding reconnects and reloads
current state. Mutations are never automatically replayed. A lost or stale
connection disables send. Preserve instance identity checks from main.

Success: boot on both simulators, exercise connection/session/conversation
navigation against a controlled server and test wire behavior through the
existing client. Real hub verification is additional when its address and
credentials are available. No messages are sent to existing real sessions
without explicit authorization to send that message.
