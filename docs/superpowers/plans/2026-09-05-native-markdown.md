# Native conversation Markdown

Jesse approved the mobile visual direction and delegated routine implementation choices. This slice applies the conversation contract to real assistant content.

Use `react-native-enriched-markdown` 1.0.2 after evaluating its published API and native behavior. It renders through platform text APIs and exposes native selection, code copy, GFM tables, font scaling and link callbacks. Keep user input literal. Keep assistant text on the reading surface with existing system typography, restrained headings, inset quotes and bounded code/table overflow. Task-list checkboxes are read-only because transcript content is authoritative server state.

Open valid HTTP(S) links through the OS. Long press reveals the exact target with copy/open actions. Unsupported local paths remain copyable and explain that a hub-file viewer is still required; never guess that a host path refers to this phone. Native code copy uses the renderer. Full-response copy preserves original Markdown. No bearer credentials are forwarded to external images.

- [ ] Add renderer and clipboard, inspect native install and build compatibility.
- [ ] Test link-target behavior and wire rendering with matching light/dark styles, read-only task lists, uncapped font scaling and no token-by-token accessibility announcements.
- [ ] Exercise real native content: headings, lists, links, quotes, code, tables, incomplete Markdown and streaming updates. Verify code copy, link actions, selection, overflow, large text and both appearances on both platforms.
- [ ] Review and record exact checks, source/build identity, screenshots and remaining limitations.

Sources read 5 September 2026: [maintainer README](https://github.com/software-mansion/enriched-markdown/tree/main/packages/react-native-enriched-markdown), [style API](https://github.com/software-mansion/enriched-markdown/blob/main/docs/STYLES.md), [Expo 57 Clipboard](https://docs.expo.dev/versions/v57.0.0/sdk/clipboard/). Published package types are the authority for version 1.0.2. Library feature claims require native verification; this plan does not certify them.

## Native control correction

The installed renderer measured its code-copy button at roughly 27dp on Android. Keep a two-file `patch-package` patch against exact version 1.0.2: header and button minimum 48dp on Android and 44pt on iOS, center header labels, and update both native measurement paths. `postinstall` must fail if the patch cannot apply. The patch was reproduced from pristine package sources and independently reviewed. Verify geometry and code-copy behavior after rebuilding; do not infer the patched binary from source alone.
