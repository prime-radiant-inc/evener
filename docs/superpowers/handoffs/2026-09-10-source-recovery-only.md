# Original mobile source recovery snapshot — do not merge wholesale

This branch preserves the entire original `live-concepts-plan2-integrate` source at `f6614d6cc137d3498460ad6b9a5d0071aea67742`, plus byte-identical copies of its two unfinished Apple project files. Jesse requested that all work be preserved on GitHub before this laptop goes for repair on 10 September 2026.

The original worktree and its stashes were not changed. This is a recovery archive, not an implementation candidate for main. It intentionally retains the obsolete Tauri application solely so original work is not lost. Jesse does not want the Tauri application, plugins, build integration, or concept runtime landed. Static sketches are separately curated in merged PR #1104. Native iPhone delivery is PR #1096.

Extract remaining work into focused PRs against current main. Do not reset main to this branch or merge it wholesale: it includes superseded protocol/client implementations and changes already landed in small PRs. Current fixes on #1091, #1098, #1100, #1105, #1108, and #1109 supersede corresponding old versions here.

Preserved Apple file SHA-256 values:

- `mobile/src-tauri/gen/apple/app.xcodeproj/project.pbxproj`: `18ea8957e133dfe5212d80bb743d40477c6f984efadbe936f54a02272e23dc1e`
- `mobile/src-tauri/gen/apple/app_iOS/Info.plist`: `5d2466b3aa245aa71c6b1562cd4865be4f8b7ef9c97f4a099f95f682a8614814`

These are project configuration files, not signing credentials. No keychain, Apple certificate/private key, provisioning secret, node_modules, build cache, or live hub state is part of this preservation commit. Historical tests describe their recorded source only and are not a current release qualification.
