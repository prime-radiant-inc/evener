# iPhone redesign, Phase 5: New session and the Hub (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Starting a session and running the hub move into two native sheets: a New session sheet that sets host, project, model, effort, plugins and access in a few taps (spec 11), and a Hub sheet for hosts, providers, plugins, display and hubs (spec 12). When the phase ends, no screen asks you to reconnect.

**Architecture:**
- **Two modal sheets, each with its own stack.** The root routes `NewSession` and `Hub` present as iOS page sheets (`presentation: "modal"`). Each hosts a nested native stack, so pickers and detail pages push inside the sheet and Back returns to the form.
- **Pure cores, table-tested.** The setup rules live in `src/newSession/launchSetup.ts`: host changes and the access and effort labels. The host, provider and hub-update status words live in small pure modules.
- **The data layer stays.** These keep their logic, and the sheets restyle only what they render:
  - the creation store (`src/newSession.ts`);
  - the credential store and the sign-in flow;
  - the plugins store with its mutation gate and fences;
  - the transcript display config.
- **Reads that describe a host go to that host.** `evener/host/request` carries them, through the web's `hostRequest`, which the app already bundles.
- **Calm.** Sheets keep their last data while the connection is down. They disable what needs the hub and say why in one status line. No control reconnects.

**Tech Stack:** Expo SDK 57, React Native 0.86.3, React 19, TypeScript, `@react-navigation/native-stack` 7.18 over `react-native-screens` 4.26, zustand 5, vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`), `expo-symbols` and `expo-sqlite/kv-store`. This phase adds three packages:
- `expo-camera`, for the pairing scanner;
- `expo-application`, for the app's own version;
- `expo-web-browser`, for the in-app sign-in page, unless phase 3 already added it.

CocoaPods runs through Bundler 2.7.2 on Ruby 3.3.6.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`. This phase uses:
- principle 2 (Calm), the vocabulary (5), and structure and sheets (6);
- New session (11), the Hub (12), states (14) and first run (15);
- the visual system (16), data sources (17), and server additions S11, S17 and S18 (18).

Other sources:
- The roadmap: `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.
- The phase 2 plan (`2026-09-26-iphone-redesign-phase2-board.md` and its part 2): the Board this phase hangs off.
- The server plan (`2026-09-26-iphone-redesign-server-additions.md`): S11.
- The prototype's `launch.js` and `hub.js` (`docs/design/mobile/redesign/prototype/`): copy the spec leaves out. The spec wins where they differ.

## Global Constraints

- **Copy is the spec's, verbatim.** Where the spec is silent, the copy is the prototype's.
  - New session:
    - header: "Cancel", "New session", "Start"; the prompt placeholder "What should the agent do?";
    - rows "Host", "Project", "Branch", "Model" (second line "via <provider>"), "Effort" (second line "How long it thinks before acting"), "Plugins" ("10 of 14"), "Access" and "More options";
    - the access levels "Full access", "Workspace write", "Read-only" and "Restricted";
    - "Browse folders on <host>…" and "New folder";
    - the plugin checklist's footer "10 of 14 on. Plugins can't be changed after the session starts.";
    - the rejection shape "paradise-park couldn't find /home/jesse/git/evener. Choose another project." (spec 5).
  - Hub:
    - the header line "Connected · evener 0.9.412 · up to date", or "Update available" in place of "up to date";
    - host states "Connected", "Connecting…" and "Offline · reconnecting", and an amber "Offline";
    - the gray tag "Hub runs 0.9.412", and "3 live" or "3 live, out of reach";
    - the three host footers from spec 12;
    - provider states "Signed in", "Sign-in expired" and "Key set", and the sign-in sentence "this code is copied for you. Paste it when the page asks for it.";
    - "Open sign-in page";
    - Display's "Appearance" (System, Light, Dark), "Reading font" (Serif, Sans) and "Default detail level";
    - Hubs' "Scan pairing code" and "Paste pairing link";
    - About's "Update hub".
  - First run (spec 15): "Connect to your hub", "Scan pairing code", "Paste pairing link", and "In Evener on your computer, open Settings, then Mobile app.".
  - No daemon, harness, thread, source or runtime in anything on screen (principle 7).
- **Calm** (spec principle 2):
  - A control appears only when it can act.
  - No screen carries a Reconnect button, asks you to reconnect, or offers pull-to-refresh. Today's administration screens (ruling 12) keep their own controls until #2539, except that they stop asking you to reconnect (Task 27).
  - Nothing asks you to do what the app does itself.
- **Color:** only from `useColors().palette`.
  - Amber (`attentionInk`) only where a human is needed: an offline host, "Sign-in expired", Start blocked by an offline host.
  - Red (`dangerInk`) for failures and destructive rows: a host's last error, a blocking plugin problem, Remove.
  - Blue (`accent`, `accentInk`) for tappable, selected and switches; a selected segment is `accentBg` with `accentInk`.
  - Version drift is a gray tag (`inkMid` on `inset`).
  - Row glyphs are bare SF Symbols in `inkMid`, never colored tiles (spec 12).
- **SF Symbols** through `SymbolView` from `expo-symbols`:
  - from spec 16.5: `server.rack` (host), `folder` (project), `arrow.triangle.branch` (branch), `cpu` (model), `puzzlepiece.extension` (plugins), `lock.shield` (access), `point.3.connected.trianglepath.dotted` (hubs) and `square.and.pencil` (New session);
  - chosen here where the spec names none: `key` (providers), `textformat.size` (display), `info.circle` (about and a hub's details), `qrcode.viewfinder` (scan), `doc.on.clipboard` (paste), `checkmark`, `chevron.right`, `eye` (vision) and `wrench.and.screwdriver` (tools).
  - A name `expo-symbols`' `SFSymbol` type rejects fails `npm run check`; pick a listed neighbor rather than casting.
- **Type:**
  - SF Pro for everything you operate.
  - Section labels: semibold 12, uppercase, +0.06em (0.72pt), `inkMid`.
  - Menlo only for machine text: paths, model ids, a host's roots and last error, and marketplace and provider-profile group labels, which stay as typed and are never uppercased (spec 11).
  - Counts use tabular figures.
- **Layout:**
  - Grouped lists: `canvas` behind, rows on `surface` in 12pt-radius groups inset 16pt, hairlines between rows.
  - Rows are at least 44pt with 16pt padding.
- **Routes and storage:**
  - New root route `Hub: NavigatorScreenParams<HubRoutes>` (Task 3).
  - `NewSession` keeps its params `{ hubId, hubName }`, adds an optional `like` (Task 20; Task 25 sends it), and presents as a modal sheet.
  - Existing storage keys don't change.
  - New kv-store keys:
    - `evener.native.display` (device-wide, never cleared with a hub);
    - `evener.native.launch-history.${hubId}`, removed by `ConnectionProvider.removeHub`.
  - The creation draft gains one optional field, `source` (ruling 28).
- **Tests** meet the hub at the request boundary: a fake `request`/`onNotification` client, or `scriptedClient` from `src/renderNative.testkit.tsx`.
  - Screens render through `render` and `renderedText` from the same kit, with `react-native` mocked by `nativeModuleMock()` and `expo-symbols` by `{ SymbolView: "SymbolView" }`.
  - Never mock the module under test.
- **Gates per task** (the roadmap and the PR-owner protocol):
  - the task's own tests (`cd mobile-native && npx vitest run <files>`);
  - `npm run check` in `mobile-native`;
  - `make test-native-bundle` when imports, `metro.config.js` or `app.json` change;
  - `cd cmd/evener-hub/frontend && npx vitest run <files>` for a task that touches web sources.
  - Never run the full `make test-native`, `make test-web` or `make lint` locally; CI runs them.
- **Repo rules:**
  - Never run Biome in `mobile-native/`.
  - Never run `npm ci` through a symlinked `node_modules`; check `[ -L node_modules ]` first.
  - Never `git add -A`.
  - iPhone only.
  - New files use tabs, like `src/board/`; an edited file keeps its own indentation (`src/newSession.ts` and `src/PluginsScreen.tsx` use two spaces).
- **Native dependencies** are added with `npx expo install <name>` from `mobile-native`.
  - The CocoaPods lock is regenerated exactly as phase 2's Task 4 Step 6 does, and `ios/` stays untracked.
  - The lock's diff adds only the new pods; anything else changing stops the task (`docs/design/mobile/ios-build-distribution.md`).

## Built on earlier phases

The roadmap starts this phase once phase 4's PRs are on main. Phases 2 to 4 are planned in parallel with this plan, so a few names below are what those plans are expected to produce; phase 3's rows name what its plan (`2026-09-26-iphone-redesign-phase3-session.md`, on main) says it builds. Before a task that uses one, read the landed code. If a module isn't there, build the fallback in the right-hand column in that task's PR.

| This plan needs | From | Used by | If it isn't on main |
|---|---|---|---|
| `connectionStatus(state, fatal, downSince, lastLiveAt, now)` in `src/board/connectionStatus.ts`, and the clock in `BoardToolbar.tsx` | phase 2, Task 7 | Task 2 | Build `connectionStatus` as phase 2's Task 7 specifies. |
| The Board's hub button and New session button (`src/board/BoardScreen.tsx`, `BoardToolbar.tsx`) | phase 2, Task 7 | Tasks 3 and 20 | Stop: phase 2 must land first. |
| The notices' actions (`src/board/notices.ts`, `src/board/Notices.tsx`) | phase 2, Task 14 | Tasks 11, 13 and 15 | Skip that task's rewire step and say so in the PR. |
| `expo-symbols` | phase 2, Task 4 | every screen | Stop: phase 2 must land first. |
| `DETAIL_LEVELS` in `src/session/detailLevels.ts`: spec 8.2's table as `{ level, label, description }[]` | phase 3, Task 12 | Task 7 | Create that module with spec 8.2's table in that shape, and use it there. |
| The model list (recent first, then grouped by provider) | phase 3, Task 21: the list `src/ModelPicker.tsx` renders, which its `ModelSheet` route shows (spec 8.5). Reuse the list, never the route, which reads a session's host (ruling 27) | Task 21 | Build the list in `src/newSession/ModelList.tsx` (Task 21). |
| `expo-web-browser` (SFSafariViewController) | phase 3, Notes & links (spec 8.8) | Task 14 | `npx expo install expo-web-browser`, then regenerate the pod lock. |
| The session's ⋯ menu | phase 3 (spec 8.7) | Task 25 | Add the item where the session screen builds its menu today. |

## Rulings

Decisions this plan makes where the spec is silent, or where the data it wants doesn't exist yet. The roadmap edits that go with rulings 11 and 26, and ruling 26's rows in spec 18, are in this plan's PR.

1. **The sheets are modal with a nested stack.**
   - `presentation: "modal"` is the native-stack presentation documented to host a nested stack (`@react-navigation/native-stack`'s types: "this also allows for a nested stack to be rendered inside the screen"). `formSheet`'s detents make no such promise.
   - Pickers and detail pages push inside the sheet with a Back to the form, as iOS's own forms do (Calendar's New Event). A sheet over a sheet over the Board would stack three layers.
   - The spec's medium detent stays with the session's own pickers (phase 3's ruling 37, on phase 2's native sheets; ruling 27).
2. **A host's reads go to that host.**
   - `model/list`, `evener/projects/recent`, `evener/paths/complete`, `evener/path/validate`, `evener/dirs/create`, `evener/git/head`, `evener/plugin/preview` and `evener/launch/resolve` take no host. Each answers for the machine that receives it.
   - For another host, the phone wraps them in `evener/host/request { host, method, params }`. The hub forwards exactly these methods (`remoteHostAdminMethods`, `cmd/evener-hub/app_host_admin.go`; the spawn form's subset is `cmd/evener-hub/host_request_methods.txt`).
   - The phone uses the web's `hostRequest` (`cmd/evener-hub/frontend/src/stores/hostRouting.ts`), which it already bundles through `usePluginPreview`. Task 18 narrows its client parameter to `Pick<AppwireClientLike, "request">`, which every caller already satisfies.
   - `thread/start` carries `source` for another host and omits it for the hub's own machine.
   - Moving `hostRouting.ts` into `@evener/appwire-client` touches 37 web importers. That is SDK-migration work, and this phase leaves it out.
3. **The hub's own machine is named after the hub.** The manifest labels the local source "this host" (`apiTreeSources`, `cmd/evener-hub/web_api_tree.go`). On the phone it shows the name you gave the hub, the Board header's name: "magic-kingdom" in the spec's frames.
4. **Host states come from `HostRow`.** No host lifecycle notification exists, so a hosts page polls `evener/host/list` every 2 seconds while it is on screen, as the web does (`HOST_POLL_MS`, `panes/settings/sections/hosts.tsx`). The states, from the facts:
   - attached: "Connected".
   - The phone's own Connect in flight: "Connecting…".
   - `midAttach` set: "Offline · reconnecting". The hub keeps `midAttach` set while its supervisor retries a dropped host (`observe`, `cmd/evener-hub/app_host_manage.go`).
   - Anything else: "Offline", with Connect.
   - An unattached host's `lastAttachError` shows as its last error. The spec's list also names an "Error" state, but section 5 rules out "error" as a state word.
   - "Last seen 2d" waits for the hub to record when a host was last reachable. No such fact exists, as the server plan's S11 notes.
   - "Hub runs 0.9.412" shows whenever the host's last-known `hubVersion` differs from the hub's. The drift footer shows only while the host is attached, since an offline host's footer is about reaching it.
5. **Live counts per host come from the Live section.** No hosts catalog exists. Task 10 reads every Live page (at most 10, 500 rows) and counts rows by `host_id`. The hub keeps an offline host's last rows, marked `offline`, so "3 live, out of reach" counts them.
6. **Provider states.**
   - `evener/auth/list` reporting `needsLogin`: "Sign-in expired".
   - An OAuth credential: "Signed in".
   - A key from any source: "Key set".
   - No credential: "Not signed in" for an OAuth provider, "No key" for a key provider, "No sign-in needed" when none is required.
   - Only "Sign-in expired" is amber.
   - "Expires in 3d" waits for the sign-in expiry the hub doesn't expose (S11b, the server plan's PR 23). Jesse's answer on S11 notes that no refresh-token lifetime exists anywhere, so the hub may never report one. Until it does, a signed-in provider shows no expiry.
   - #2483 (on main) sets `needsLogin` only when the access token has expired and no refresh token can renew it. A rejected refresh isn't recorded until #2479, and until then the phone can't say so.
7. **Plugins show no update badge.** `PluginEntry` carries no available version (`appwire/types.go`). Upgrade stays in a plugin's detail. Its result says "Already up to date" when version and commit didn't change, or "Upgraded to <version>".
8. **"Show model on Board rows" waits for S17.** Navigation summaries carry no model (phase 2, ruling 6). S17 is the server item that will add the model's display name to them (ruling 26). Until it lands, the toggle would change nothing, which breaks principle 2, so Display leaves it out.
9. **Details open where their state lives.**
   - A plugin's and a provider's details open as sheets over their list, as today. Their mutation gates, fences and sign-in flows live with the list (`PluginsScreen.tsx` around line 90, `ProvidersScreen.tsx` around line 105), and splitting them across pages would put that machinery at risk.
   - A host's detail pushes as a page, because the hosts controller is shared through the Hub sheet's context.
10. **Interim rows during the phase.** Until a page lands, its Hub row closes the sheet and opens today's screen on the root stack, with `StackActions.replace`. The same pattern served phase 2's PR 2 and keeps everything reachable at every merge:
    - Providers opens `Providers`;
    - Plugins opens `Plugins`;
    - Display opens `TranscriptPreferences`;
    - Hubs opens the root `Hubs`;
    - "Hub settings" opens `HubSettings` (keyboard shortcuts, launch defaults and the upgrade live there today).
11. **In-app alerts ship with phase 6.** The Alerts page's five toggles control banners, holds and haptics that phase 6 builds, and a toggle that controls nothing breaks principle 2. The coordinator ruled this way on 2026-09-26. This PR moves the Alerts page from the roadmap's phase 5 row to phase 6's.
12. **Today's administration screens stay reachable from the Hub** (spec 12's More). Jesse, 2026-09-26: "We *should* allow full admin on the phone, but that can be a later phase." So this phase deletes none of them:
    - keyboard shortcuts (`KeybindingPreferences`);
    - launch defaults and project launch settings (`LaunchSettings`);
    - the hub's runtime, storage, agents and MCP servers (today's `HubSettings`, less its update section and its links, Task 5);
    - adding, editing, removing and re-defaulting provider instances (Providers' MANAGE groups, Task 13).

    The Hub's MORE group links the first three, with the rows "Keyboard shortcuts", "Launch defaults" and "Hub settings" (Task 5). Full administration in the redesign's language is a later phase (#2539). Until then these screens keep their own controls and layout, except the Reconnect copy Task 27 retires.
13. **The harness choice goes.** The hub offers one harness, `evener` (`launchHarnessDescriptors`, `cmd/evener-hub/app_models.go`), and principle 7 keeps the word off screen. The sheet sends no `harness`. The draft keeps writing `harness: ""`, so its stored shape doesn't change.
14. **Access and More options are launch overrides.**
    - Access writes `sandbox`: `off` is Full access, then `workspace-write`, `read-only` and `restricted`.
    - `sandboxNet` shows as "Network" only when a sandbox mode is set, since it has no effect otherwise (`sandbox_net`, `cmd/evener-hub/internal/launchconfig/schema.go`).
    - More options writes `contextStrategy`, `maxSubagentDepth` and `maxRounds`. The last is labelled "Max turns", spec 11's word; the schema calls it "Max rounds".
    - Each row names the hub's default for the chosen host and project, from `evener/launch/resolve`'s `effective` with the sheet's own overrides left out.
15. **Branch shows the current branch until S18.** `evener/git/head` gives the project's branch. Today the hub has no way to start a session in a new worktree (`ThreadStartParams` has no such field), and S18 is the server item for it (ruling 26). Until it lands, the Branch row is information only.
16. **The phone remembers its starts,** per hub.
    - Every successful Start records its setup.
    - The sheet opens on the newest setup started on this hub. With nothing ever started here, the sheet starts from the hub's defaults.
17. **Changing host keeps the project when the new host has it** (`evener/path/validate` with `kind: "dir"` on that host). Otherwise the project moves to the host's most recent one, and the Host row's footer says so, as the prototype words it: "evener isn't on paradise-park, so the project changed to docs." With no recent project there, it says "evener isn't on paradise-park. Choose a project."
18. **Only Cancel discards.** The draft saves as you type (`creationDraftRepository.ts`), so swiping the sheet down closes it and keeps the draft for next time.
    - Cancel with a prompt or an image asks "Delete this draft?" with "Delete draft" (destructive) and "Keep draft". A setup alone is never lost, because the sheet opens on the newest start.
    - Spec 6's "ask before discarding" holds, because the only way to discard asks.
19. **The prompt stays optional**, as the app allows today: the hub starts an empty session. The prototype's disabled Start isn't carried over.
20. **Start blames a field only for what the phone checked.**
    - An offline or unknown host blames Host.
    - A project the host couldn't find blames Project.
    - A blocking plugin problem blames Plugins.
    - A hub rejection after Start shows the hub's reason inline above the rows. `ThreadStartParams` rejections carry no field.
21. **Connection state in sheets.**
    - Each sheet shows spec 14's words on one line: "Reconnecting…", "Offline · updated 3m ago", "Update needed". The line comes from phase 2's `connectionStatus`, through a hook extracted from `BoardToolbar` (Task 2).
    - Pages keep their last data. Controls that need the hub are disabled, not hidden, so a half-typed form keeps its buttons.
    - A page that has never loaded says "Connecting to <hub>…" instead of today's walls.
22. **Hub updates.**
    - The header's "up to date" or "Update available" comes from `evener/update/check`, read when the Hub sheet opens.
    - About's "Update hub" appears only when an update is available. It confirms, then runs `evener/update/apply`, which installs and restarts the hub as the web's Updates section does. The phone reconnects on its own (`hubConnection.ts`), and the next check reads the new version.
    - Jesse, 2026-09-26: "hub update: install and restart as the web: yes. should use a shared api." So the check and the apply run through one update client in `@evener/appwire-client`, lifted from the web's store (`cmd/evener-hub/frontend/src/stores/hubUpdate.ts`) before the phone points at it (Task 4). The phone's install-only `evener/upgrade` path goes (Task 5).
    - About doesn't repeat the hub's version (spec 12). It is a group on the Hub's home, not a page.
23. **First run lands here.** Spec 15 isn't on any phase's row, and its parts are this phase's: the scanner, pasting a link, and the saved-hub list. The root `Hubs` route keeps its name, since restore depends on it (`restoredStack`, `src/location.ts`), and shows spec 15's screen. Entering an address and token by hand stays as a quiet third choice, "Enter the address", so a hub without a pairing page is still reachable.
24. **"New session like this" copies what the wire has.** It copies the host (the ref's source), project (`cwd`), model (`modelProvider`) and effort (`reasoningEffort`). A session's plugins and access aren't on `Thread`, so those come from the newest remembered start. Spec 8.7's menu doesn't list the item; spec 11 puts it there, so Task 25 adds it.
25. **The Board's notices open the Hub at the thing they name**, each once its page lands:
    - a host's "Details" opens Hosts and pushes that host;
    - "Plugins" opens Plugins with that plugin's detail;
    - "Sign in" opens Providers with that provider's sign-in.

    Each call is `navigation.navigate("Hub", { screen, params, initial: false })`. That keeps the Hub's home under the page, so Back works inside the sheet.
26. **Two server items join the lane.** Two spec items need hub work that no server item covered. The coordinator ruled on 2026-09-26 that both join the server lane. Phase 3's plan took S15 and S16, so these are:
    - S17: the model's display name on navigation summaries, for "Show model on Board rows" (ruling 8);
    - S18: starting a session in a new worktree branch, for spec 11's Branch (ruling 15).

    This PR adds both to spec 18's table and to the roadmap's phase 7 row. This phase ships on their fallbacks: no model toggle in Display, and a Branch row that only shows the current branch. The phone switches when each lands, as the roadmap's phase 7 does for every item.
27. **Native sheets, and the sheets this phase keeps as they are.** Jesse approved native sheets that open pickers at half height ("Build the redesign's sheets as native navigation sheets, so pickers open at half height?" "yes"). Phase 2's PR 6 builds them as native-stack formSheet routes (its ruling 28), and phases 3 and 4 build their detented sheets on them. This phase's sheets keep the shapes rulings 1 and 9 give them, because a formSheet doesn't fit them:
    - Hub and New session stay `presentation: "modal"` routes, each holding its own nested stack (ruling 1). The pickers and detail pages inside push within the sheet, so they need no detents of their own.
    - A provider's and a plugin's detail sheets, and sign-in, stay RN `Modal` page sheets over their lists (ruling 9), because their mutation gates, fences and sign-in flows live with the list.
    - Presenting a formSheet route dismisses any other presented view controller (phase 2's ruling 28), so nothing inside Hub or New session opens one. "New session like this" (Task 25) opens New session from the session's ⋯ menu, a native menu, not from a sheet.
    - A relaunch skips only formSheet routes (`routeToSave`, phase 2's Task 18.1). With Hub or New session open, it reopens the Board, as it does today (`locationForRoute` gives both `{ hubId }`).
    - Phase 3's model sheet is a session sheet that reads its session's host. New session's model picker (Task 21) reuses its list, never the route.
28. **Drafts saved before this phase still load.** The saved creation draft's new `source` field is optional, and absent means the hub's own machine (Task 19). This is backward compatibility for stored data, which Jesse allowed on 2026-09-26: "We do not need back-compat on drafts, but we don't not need it." Without it, a draft saved before this phase would fail to load with "saved creation draft could not be loaded".

## Questions for Jesse

None open. The coordinator settled question 1 (the Alerts page moves to phase 6: ruling 11) and question 5 (S17 and S18 join the server lane: ruling 26). Jesse answered the other three on 2026-09-26: today's administration screens stay reachable from the Hub, and full administration is a later phase, #2539 (question 2, ruling 12); hub updates install and restart as the web's do, through one client shared with the web (question 3, ruling 22); and drafts saved before this phase keep loading (question 4, ruling 28).

## Review Focus

1. **A session meant for another host starts there, with that host's projects, models and plugins.** Reads for paradise-park issued to the hub's own machine, or the reverse, would start a session with a path or model the host doesn't have. Pinned by Task 18: each launch read is plain for the local host and wrapped in `evener/host/request` for another. Also pinned by Task 19: `submit` sends `thread/start` with `source` only for another host.
2. **Answers for the host you just left never land in the form.** A slow `model/list` or `evener/projects/recent` for the previous host must not overwrite the new host's form. Pinned by Task 19's host-switch race test.
3. **A draft naming a host that is offline or gone never starts anywhere else.** The web learned this in round five of its host picker (`Spawn.tsx`, the `submittedSource` comment). Pinned by Task 20:
   - with the draft's host offline, Start is disabled, the Host row says "Offline", the sheet says "paradise-park is offline. Connect it or choose another host.", and no `thread/start` is sent;
   - with the host missing from the hub's list, it says "paradise-park is no longer a host on this hub. Choose another host."
4. **The connection drops with a sheet open.** Content stays. Controls that need the hub disable. The status line says "Reconnecting…" after 2 seconds, and no element anywhere reads "Reconnect".
   - Pinned by Task 3: the Hub home keeps its rows with the client gone.
   - Pinned by Task 20: the New session keeps the typed prompt and disables Start.
   - Pinned by Task 27: a source guard fails the build if any production module under `mobile-native/src` renders "Reconnect".
5. **Removing a hub from inside the Hub sheet.**
   - Removing the hub you're connected to closes the sheet and returns to the first-run screen, with no stale Board.
   - A removed hub's launch history is forgotten, and no other hub's are.
   - Pinned by Task 3 (`useClosesOnHubChange` leaves for `Hubs` when no hub is selected), Task 9 (removing the selected hub from its details calls `removeHub` and leaves the navigation to the sheet) and Task 17 (`forgetLaunchMemory` leaves other hubs alone).

---

## PRs and lanes

| PR | Tasks | Model | Starts when | Lane |
|---|---|---|---|---|
| 1: the Hub sheet | 1-3 | Sonnet for 1-2, Opus (medium) for 3 | phase 4's PRs are on main | A |
| 8: the New session's rules and reads | 16-19 | Sonnet | phase 4's PRs are on main | B |
| 12: the demo hub serves New session and the Hub | 26 | Sonnet | phase 4's PRs are on main (it builds on #2471, already there) | C |
| 2: the shared hub-update client, About and hub updates | 4-5 | Opus for 4 and 5 | PR 1 lands | A |
| 3: Display | 6-7 | Sonnet for 6, Opus for 7 | PR 1 lands | A2 |
| 4: Hubs, pairing and first run | 8-9 | Opus | PR 1 lands | A3 |
| 5: Hosts | 10-11 | Sonnet for 10, Opus for 11 | PRs 1 and 2 land | A4 |
| 6: Providers | 12-14 | Sonnet for 12, Opus for 13-14 | PR 1 lands | A5 |
| 7: Plugins | 15 | Opus | PR 1 lands | A6 |
| 9: the New session sheet | 20-21 | Opus | PRs 1, 5 and 8 land | B |
| 10: plugins, access and options in New session | 22 | Opus | PR 9 lands | B |
| 11: New session like this | 25 | Opus | PR 10 lands | B |
| 13: the last Reconnect, DESIGN.md, screenshots | 27-29 | Opus | every other PR lands | A |

- After PR 1, PRs 2 to 7 can run as parallel lanes. Each touches `src/hub/HubSheet.tsx` (one route) and `src/hub/HubHome.tsx` (one row), so a rebase conflict is a line or two.
- PR 9 needs PR 5's hosts controller for the host picker.
- The phase's last PR carries Release-simulator screenshots of Appendix A frames 20-24 against the demo fleet (Task 29).
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decomposition after five rounds.

The Reconnect sites other phases own, for the coordinator:
- the conversation and its fork, queue, tasks, session sheet and session deletion editors (phase 3);
- the pinned-section screens (phase 2's part 2);
- the outbox copy in `nativeMutationHost.ts` (phase 6).

Task 27 sweeps whatever is left when this phase ends, and its guard allows only phase 6's file.

---

## PR 1: the Hub sheet

PR 1 puts the Hub sheet behind the Board's hub button, with the grouped-list pieces every later page uses. Its rows open today's screens until their pages land (ruling 10), so nothing becomes unreachable.

### Task 1: Grouped-list pieces

**Files:**
- Create: `mobile-native/src/sheet/Grouped.tsx`
- Test: `mobile-native/src/sheet/Grouped.test.tsx`

**Interfaces:**
- Produces, all exported from `Grouped.tsx`:
  - `GroupedPage({ children })`: the scrolling canvas page.
  - `GroupLabel({ children: string, machine?: boolean })`: a section label, or a Menlo one as typed.
  - `Group({ children })`: an inset group with hairlines (`testID="hairline"`) between rows.
  - `Row(props: RowProps)`:
    - props `label`, `sub?`, `machineSub?`, `icon?: SFSymbol`, `value?: ReactNode`, `chevron?`, `tone?: "normal" | "accent" | "danger"`, `checked?: boolean`, `disabled?`, `onPress?`, `accessibilityLabel?`;
    - a button when it has `onPress`, one quiet accessible element otherwise.
  - `SwitchRow({ label, sub?, icon?, value, onChange, disabled? })`.
  - `GroupFooter({ children: string, tone?: "normal" | "attention" | "danger" })`.
  - `Segmented<T extends string>({ label, options: readonly { value: T; label: string }[], value: T | null, onChange, disabled? })`.
  - `Tag({ text, tone: "amber" | "gray" | "blue" | "red" })`.

- [ ] **Step 1: Write the failing tests**

```tsx
// mobile-native/src/sheet/Grouped.test.tsx
import { describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import { Group, GroupFooter, GroupLabel, Row, Segmented, SwitchRow, Tag } from "./Grouped";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const light = palettes.light;
const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name);
const texts = (tree: ReturnType<typeof render>) => tree.root.findAllByType("Text" as never);
const merged = (style: unknown) => Object.assign({}, ...[style].flat());

describe("a row", () => {
	it("reads as its label, detail and value, and opens on a tap", () => {
		const onPress = vi.fn();
		const tree = render(
			<Row icon="server.rack" label="Host" sub="macOS · arm64" value="magic-kingdom" chevron onPress={onPress} />,
		);
		const button = tree.root.findByProps({ accessibilityRole: "button" });
		expect(button.props.accessibilityLabel).toBe("Host, macOS · arm64, magic-kingdom");
		button.props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.right"]);
	});

	it("is one quiet element when it has no action", () => {
		const tree = render(<Row label="Version" value="0.9.412" />);
		expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
		expect(renderedText(tree)).toContain("0.9.412");
	});

	it("refuses taps while disabled", () => {
		const tree = render(<Row label="Connect" tone="accent" disabled onPress={() => {}} />);
		const button = tree.root.findByProps({ accessibilityRole: "button" });
		expect(button.props.disabled).toBe(true);
		expect(button.props.accessibilityState).toEqual({ disabled: true });
	});

	it("draws a picker's check only on the chosen row", () => {
		const chosen = render(<Row label="magic-kingdom" checked onPress={() => {}} />);
		expect(symbols(chosen)).toEqual(["checkmark"]);
		expect(chosen.root.findByProps({ accessibilityRole: "button" }).props.accessibilityState).toEqual({
			disabled: false,
			selected: true,
		});
		expect(symbols(render(<Row label="paradise-park" checked={false} onPress={() => {}} />))).toEqual([]);
	});

	it("colors action and destructive labels, and sets a machine detail in Menlo", () => {
		expect(texts(render(<Row label="Connect" tone="accent" onPress={() => {}} />))[0]?.props.style.color).toBe(
			light.accentInk,
		);
		expect(texts(render(<Row label="Remove" tone="danger" onPress={() => {}} />))[0]?.props.style.color).toBe(
			light.dangerInk,
		);
		const path = render(<Row label="evener" sub="/home/jesse/git/evener" machineSub />);
		expect(merged(texts(path)[1]?.props.style).fontFamily).toBe("Menlo");
	});
});

describe("a group", () => {
	it("draws hairlines between its rows, not around them", () => {
		const tree = render(
			<Group>
				<Row label="One" />
				<Row label="Two" />
				<Row label="Three" />
			</Group>,
		);
		expect(tree.root.findAllByProps({ testID: "hairline" })).toHaveLength(2);
	});
});

describe("a switch row", () => {
	it("names its switch, tints it with the accent, and reports a flip", () => {
		const onChange = vi.fn();
		const tree = render(<SwitchRow label="Network" sub="Lets the session's commands reach the internet" value onChange={onChange} />);
		const toggle = tree.root.findByType("Switch" as never);
		expect(toggle.props.accessibilityLabel).toBe("Network");
		expect(toggle.props.trackColor.true).toBe(light.accent);
		toggle.props.onValueChange(false);
		expect(onChange).toHaveBeenCalledWith(false);
	});
});

describe("a segmented control", () => {
	const options = [
		{ value: "system", label: "System" },
		{ value: "light", label: "Light" },
		{ value: "dark", label: "Dark" },
	] as const;

	it("marks the chosen segment in the accent tint and reports a new choice", () => {
		const onChange = vi.fn();
		const tree = render(<Segmented label="Appearance" options={options} value="light" onChange={onChange} />);
		const radios = tree.root.findAllByProps({ accessibilityRole: "radio" });
		expect(radios.map((radio) => radio.props.accessibilityState.checked)).toEqual([false, true, false]);
		expect(radios[1]?.props.style.backgroundColor).toBe(light.accentBg);
		radios[2]?.props.onPress();
		expect(onChange).toHaveBeenCalledWith("dark");
	});

	it("ignores a tap on the chosen segment", () => {
		const onChange = vi.fn();
		const tree = render(<Segmented label="Appearance" options={options} value="light" onChange={onChange} />);
		tree.root.findAllByProps({ accessibilityRole: "radio" })[1]?.props.onPress();
		expect(onChange).not.toHaveBeenCalled();
	});
});

describe("labels, footers and tags", () => {
	it("uppercases a section label and keeps a machine label as typed in Menlo", () => {
		const section = merged(texts(render(<GroupLabel>Where</GroupLabel>))[0]?.props.style);
		expect(section).toMatchObject({ textTransform: "uppercase", fontWeight: "600", letterSpacing: 0.72 });
		const machine = merged(
			texts(render(<GroupLabel machine>superpowers-marketplace</GroupLabel>))[0]?.props.style,
		);
		expect(machine.fontFamily).toBe("Menlo");
		expect(machine.textTransform).toBeUndefined();
	});

	it("colors a footer by what it reports", () => {
		expect(texts(render(<GroupFooter tone="danger">paradise-park is offline.</GroupFooter>))[0]?.props.style.color).toBe(
			light.dangerInk,
		);
	});

	it("draws version drift as a gray tag", () => {
		expect(texts(render(<Tag text="Hub runs 0.9.412" tone="gray" />))[0]?.props.style).toMatchObject({
			color: light.inkMid,
			backgroundColor: light.inset,
		});
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/sheet/Grouped.test.tsx`
Expected: FAIL: `Cannot find module './Grouped'`.

- [ ] **Step 3: Implement**

```tsx
// mobile-native/src/sheet/Grouped.tsx
// The grouped list the Hub and New session sheets are built from (spec 12 and
// 16): uppercase section labels, inset groups of rows on the surface color over
// the canvas, and ink-low footers. A row carries a bare SF Symbol in ink-mid
// (never a colored tile), a label with an optional second line, a trailing
// value, and a chevron when it opens a page.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { Children, Fragment, isValidElement, type ReactNode } from "react";
import {
	Platform,
	Pressable,
	ScrollView,
	Switch,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { fonts } from "../design/tokens";
import { useColors } from "../ui";

/** iOS text is scaled by hand, as ui.tsx does, so measurement and drawing
 * agree when Dynamic Type changes under a mounted row. */
function useTextScale(): number {
	const { fontScale } = useWindowDimensions();
	return Platform.OS === "ios" ? fontScale : 1;
}

/** The scrolling page a grouped sheet page sits in. */
export function GroupedPage({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	return (
		<ScrollView
			style={{ flex: 1, backgroundColor: palette.canvas }}
			contentContainerStyle={{ paddingBottom: 32 }}
			keyboardShouldPersistTaps="handled"
			automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
		>
			{children}
		</ScrollView>
	);
}

/** A section label: SF Pro semibold 12, uppercase, +0.06em (spec 16.2). A
 * machine label (a marketplace, a provider profile) is Menlo as typed and never
 * uppercased (spec 11). */
export function GroupLabel({ children, machine = false }: { children: string; machine?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			accessibilityRole="header"
			allowFontScaling={Platform.OS !== "ios"}
			style={[
				{
					color: palette.inkMid,
					fontSize: 12 * scale,
					lineHeight: 16 * scale,
					paddingHorizontal: 32,
					paddingTop: 20,
					paddingBottom: 6,
				},
				machine
					? { fontFamily: fonts.mono }
					: ({ fontWeight: "600", letterSpacing: 0.72, textTransform: "uppercase" } as const),
			]}
		>
			{children}
		</Text>
	);
}

/** An inset group of rows with hairlines between them, not around them. */
export function Group({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	const rows = Children.toArray(children);
	return (
		<View style={{ marginHorizontal: 16, borderRadius: 12, overflow: "hidden", backgroundColor: palette.surface }}>
			{rows.map((row, index) => (
				<Fragment key={isValidElement(row) && row.key !== null ? row.key : `row-${index}`}>
					{index > 0 ? (
						<View testID="hairline" style={{ height: 0.5, marginLeft: 16, backgroundColor: palette.edge }} />
					) : null}
					{row}
				</Fragment>
			))}
		</View>
	);
}

export interface RowProps {
	label: string;
	/** A second line in ink-low. */
	sub?: string;
	/** The second line is a path or an id: Menlo. */
	machineSub?: boolean;
	icon?: SFSymbol;
	/** The trailing value: text in ink-mid, or a node such as a tag. */
	value?: ReactNode;
	/** The row opens a page. */
	chevron?: boolean;
	/** "accent" for an action ("Connect", "Browse folders on …"), "danger"
	 * for a destructive one ("Remove"). */
	tone?: "normal" | "accent" | "danger";
	/** A picker's row: true draws the check, false leaves the space. */
	checked?: boolean;
	disabled?: boolean;
	onPress?: () => void;
	/** VoiceOver's reading when the visible text isn't enough. */
	accessibilityLabel?: string;
}

export function Row({
	label,
	sub,
	machineSub = false,
	icon,
	value,
	chevron = false,
	tone = "normal",
	checked,
	disabled = false,
	onPress,
	accessibilityLabel,
}: RowProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const labelColor =
		tone === "accent" ? palette.accentInk : tone === "danger" ? palette.dangerInk : palette.inkHi;
	const plainValue = typeof value === "string" || typeof value === "number" ? String(value) : undefined;
	const reading = accessibilityLabel ?? [label, sub, plainValue].filter(Boolean).join(", ");
	const body = (
		<>
			{checked !== undefined ? (
				<View style={{ width: 22, alignItems: "center" }}>
					{checked ? <SymbolView name="checkmark" tintColor={palette.accentInk} size={17} /> : null}
				</View>
			) : icon ? (
				<View style={{ width: 22, alignItems: "center" }}>
					<SymbolView name={icon} tintColor={palette.inkMid} size={17} />
				</View>
			) : null}
			<View style={{ flex: 1, gap: 2 }}>
				<Text
					allowFontScaling={Platform.OS !== "ios"}
					style={{ color: labelColor, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{label}
				</Text>
				{sub ? (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={[
							{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale },
							machineSub ? { fontFamily: fonts.mono } : null,
						]}
					>
						{sub}
					</Text>
				) : null}
			</View>
			{plainValue !== undefined ? (
				<Text
					allowFontScaling={Platform.OS !== "ios"}
					style={{ color: palette.inkMid, fontSize: 17 * scale, fontVariant: ["tabular-nums"] }}
				>
					{plainValue}
				</Text>
			) : (
				(value ?? null)
			)}
			{chevron ? <SymbolView name="chevron.right" tintColor={palette.inkLow} size={13} /> : null}
		</>
	);
	const style = {
		flexDirection: "row",
		alignItems: "center",
		gap: 12,
		minHeight: 44,
		paddingHorizontal: 16,
		paddingVertical: 11,
		opacity: disabled ? 0.4 : 1,
	} as const;
	if (!onPress)
		return (
			<View accessible accessibilityLabel={reading} style={style}>
				{body}
			</View>
		);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={reading}
			accessibilityState={checked === undefined ? { disabled } : { disabled, selected: checked }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => [style, pressed ? { backgroundColor: palette.pressed } : null]}
		>
			{body}
		</Pressable>
	);
}

/** A row whose trailing control is a switch in the accent color (spec 16.1:
 * switches are accent, never the working green). The switch is the accessible
 * element, so VoiceOver can flip it. */
export function SwitchRow({
	label,
	sub,
	icon,
	value,
	onChange,
	disabled = false,
}: {
	label: string;
	sub?: string;
	icon?: SFSymbol;
	value: boolean;
	onChange(value: boolean): void;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 12, minHeight: 44, paddingHorizontal: 16, paddingVertical: 8 }}>
			{icon ? (
				<View style={{ width: 22, alignItems: "center" }}>
					<SymbolView name={icon} tintColor={palette.inkMid} size={17} />
				</View>
			) : null}
			<View style={{ flex: 1, gap: 2 }} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
				<Text allowFontScaling={Platform.OS !== "ios"} style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}>
					{label}
				</Text>
				{sub ? (
					<Text allowFontScaling={Platform.OS !== "ios"} style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale }}>
						{sub}
					</Text>
				) : null}
			</View>
			<Switch
				accessibilityLabel={label}
				accessibilityHint={sub}
				value={value}
				disabled={disabled}
				onValueChange={onChange}
				trackColor={{ false: palette.edgeStrong, true: palette.accent }}
			/>
		</View>
	);
}

/** A group's footer: ink-low, or the attention or danger ink when it reports
 * something a person must act on. */
export function GroupFooter({
	children,
	tone = "normal",
}: {
	children: string;
	tone?: "normal" | "attention" | "danger";
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "attention" ? palette.attentionInk : tone === "danger" ? palette.dangerInk : palette.inkLow;
	return (
		<Text
			allowFontScaling={Platform.OS !== "ios"}
			style={{ color, fontSize: 13 * scale, lineHeight: 18 * scale, paddingHorizontal: 32, paddingTop: 6, paddingBottom: 8 }}
		>
			{children}
		</Text>
	);
}

/** A segmented control (spec 16.1: the chosen segment is accent-bg with accent
 * ink, never an ink fill). A tap on the chosen segment does nothing. */
export function Segmented<T extends string>({
	label,
	options,
	value,
	onChange,
	disabled = false,
}: {
	label: string;
	options: readonly { value: T; label: string }[];
	value: T | null;
	onChange(value: T): void;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			accessibilityRole="radiogroup"
			accessibilityLabel={label}
			style={{
				flexDirection: "row",
				gap: 2,
				marginHorizontal: 16,
				padding: 2,
				borderRadius: 22,
				backgroundColor: palette.inset,
				opacity: disabled ? 0.4 : 1,
			}}
		>
			{options.map((option) => {
				const chosen = option.value === value;
				return (
					<Pressable
						key={option.value}
						accessibilityRole="radio"
						accessibilityLabel={option.label}
						accessibilityState={{ checked: chosen, disabled }}
						disabled={disabled}
						onPress={() => {
							if (!chosen) onChange(option.value);
						}}
						style={{
							flex: 1,
							minHeight: 40,
							alignItems: "center",
							justifyContent: "center",
							borderRadius: 20,
							backgroundColor: chosen ? palette.accentBg : "transparent",
						}}
					>
						<Text
							allowFontScaling={Platform.OS !== "ios"}
							numberOfLines={1}
							style={{
								color: chosen ? palette.accentInk : palette.inkHi,
								fontSize: 15 * scale,
								fontWeight: chosen ? "600" : "400",
							}}
						>
							{option.label}
						</Text>
					</Pressable>
				);
			})}
		</View>
	);
}

const TAG_TONES = {
	amber: ["attentionInk", "attentionBg"],
	gray: ["inkMid", "inset"],
	blue: ["accentInk", "accentBg"],
	red: ["dangerInk", "dangerBg"],
} as const;

/** A small capsule beside a value: an amber "Offline", a gray "Hub runs
 * 0.9.412" (spec 16.1: version drift is a gray tag). */
export function Tag({ text, tone }: { text: string; tone: keyof typeof TAG_TONES }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [ink, fill] = TAG_TONES[tone];
	return (
		<Text
			allowFontScaling={Platform.OS !== "ios"}
			style={{
				color: palette[ink],
				backgroundColor: palette[fill],
				fontSize: 11 * scale,
				lineHeight: 13 * scale,
				fontWeight: "600",
				paddingHorizontal: 5,
				paddingVertical: 2,
				borderRadius: 4,
				overflow: "hidden",
			}}
		>
			{text}
		</Text>
	);
}
```

- [ ] **Step 4: Run the tests and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/sheet/Grouped.test.tsx && npm run check`
Expected: PASS, and `tsc` reports nothing.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/sheet/Grouped.tsx mobile-native/src/sheet/Grouped.test.tsx
git commit -m "feat(native): grouped-list pieces for the redesign's sheets"
```

### Task 2: One connection line for the Board and every sheet

Phase 2's `BoardToolbar` keeps the clock that turns `connectionStatus` into "Reconnecting…" at 2 seconds and "Offline · updated 3m ago" at 30. The sheets need the same words at the same times. This task moves that clock into a hook both use, and adds the sheet's two small pieces:
- `SheetStatus`: the line itself;
- `Connecting`: what a page that has never loaded says in place of today's walls.

Phase 3's Task 15 plans the same extraction, under the same name, `useConnectionStatusText`, and wires `BoardToolbar` to it: whichever of the two phases lands first builds the hook and that wiring, so there is one hook, not two. If it's already there by the time this task runs, its "add the hook" step is a no-op; this task's real job is the sheet's two pieces below and pointing them, and `HubHome` (Task 3), at the existing hook.

**Files:**
- Modify: `mobile-native/src/board/connectionStatus.ts` (add `useConnectionStatusText`, unless phase 3's Task 15 already has)
- Modify: `mobile-native/src/board/BoardToolbar.tsx` (call the hook; delete its own clock; unless phase 3's Task 15 already did this)
- Modify: `mobile-native/src/connectionRecovery.ts` (export `INCOMPATIBLE_VERSIONS`)
- Create: `mobile-native/src/sheet/SheetStatus.tsx`
- Test: `mobile-native/src/board/connectionStatusText.test.tsx` and `mobile-native/src/sheet/SheetStatus.test.tsx`

**Interfaces:**
- Consumes: `connectionStatus(state, fatal, downSince, lastLiveAt, now)` (phase 2, Task 7) and `useConnection()` (`state`, `fatal`).
- Produces:
  - `useConnectionStatusText(): string | null`;
  - `SheetStatus()`;
  - `Connecting({ hubName: string })`;
  - `INCOMPATIBLE_VERSIONS` in `src/connectionRecovery.ts`: spec 14's sentence. It lives in that module because the module imports no React Native, so Task 27 can use it in `connectionFailure` without pulling the UI into `connection.test.ts`, which runs with no `react-native` mock. If phase 2 already exported a constant for the sentence (grep `need compatible versions` in `src`), use it when its module imports no React Native; otherwise move it into `connectionRecovery.ts`. Never declare a second one.

- [ ] **Step 1: Write the failing tests**

```tsx
// mobile-native/src/board/connectionStatusText.test.tsx
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { useConnectionStatusText } from "./connectionStatus";

const connection = { state: "ready", fatal: false };
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

beforeEach(() => {
	vi.useFakeTimers();
	connection.state = "ready";
	connection.fatal = false;
});
afterEach(() => vi.useRealTimers());

it("says nothing while live and runs no clock", () => {
	const hook = renderHook(useConnectionStatusText);
	expect(hook.result.current).toBeNull();
	expect(vi.getTimerCount()).toBe(0);
});

it("says Reconnecting after 2 seconds down and Offline after 30", () => {
	const hook = renderHook(useConnectionStatusText);
	connection.state = "reconnecting";
	hook.rerender();
	act(() => {
		vi.advanceTimersByTime(1_000);
	});
	expect(hook.result.current).toBeNull();
	act(() => {
		vi.advanceTimersByTime(1_000);
	});
	expect(hook.result.current).toBe("Reconnecting…");
	act(() => {
		vi.advanceTimersByTime(28_000);
	});
	expect(hook.result.current).toMatch(/^Offline/);
});

it("goes quiet and stops its clock when the connection is back", () => {
	const hook = renderHook(useConnectionStatusText);
	connection.state = "closed";
	hook.rerender();
	act(() => {
		vi.advanceTimersByTime(3_000);
	});
	connection.state = "ready";
	hook.rerender();
	expect(hook.result.current).toBeNull();
	expect(vi.getTimerCount()).toBe(0);
});

it("says Update needed at once for a close no retry can fix", () => {
	const hook = renderHook(useConnectionStatusText);
	connection.state = "closed";
	connection.fatal = true;
	hook.rerender();
	expect(hook.result.current).toBe("Update needed");
});
```

```tsx
// mobile-native/src/sheet/SheetStatus.test.tsx
import { expect, it, vi } from "vitest";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { render, renderedText } from "../renderNative.testkit";
import { Connecting, SheetStatus } from "./SheetStatus";

const status = { line: null as string | null, fatal: false };
vi.mock("../board/connectionStatus", () => ({ useConnectionStatusText: () => status.line }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ fatal: status.fatal }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("shows nothing while live, and the line with no button while not", () => {
	status.line = null;
	expect(render(<SheetStatus />).toJSON()).toBeNull();
	status.line = "Reconnecting…";
	const tree = render(<SheetStatus />);
	expect(renderedText(tree)).toBe("Reconnecting…");
	expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
});

it("says a never-loaded page is connecting, or why it can't", () => {
	status.fatal = false;
	expect(renderedText(render(<Connecting hubName="magic-kingdom" />))).toBe("Connecting to magic-kingdom…");
	status.fatal = true;
	expect(renderedText(render(<Connecting hubName="magic-kingdom" />))).toBe(INCOMPATIBLE_VERSIONS);
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/connectionStatusText.test.tsx src/sheet/SheetStatus.test.tsx`
Expected: FAIL: `useConnectionStatusText` isn't exported, and `./SheetStatus` doesn't exist.

- [ ] **Step 3: Implement**

Add the hook to `src/board/connectionStatus.ts`, unless phase 3's Task 15 already added `useConnectionStatusText` there and wired `BoardToolbar` to it, in which case skip straight to the sheet pieces below. Otherwise it's the clock `BoardToolbar` runs today: if `BoardToolbar`'s clock differs from this code, move its version instead and keep these tests: the tests are the contract.

```ts
// added to mobile-native/src/board/connectionStatus.ts
import { useEffect, useRef, useState } from "react";
import { useConnection } from "../ConnectionProvider";

/** The connection's words for any screen (spec 14): null while live,
 * "Reconnecting…" after 2 seconds down, "Offline · updated 3m ago" after 30,
 * "Update needed" for a close no retry can fix. It re-renders at the 2- and
 * 30-second marks and then once a minute; a live connection runs no clock.
 * The Board's toolbar and every sheet read this one hook, so they never
 * disagree about the connection. */
export function useConnectionStatusText(): string | null {
	const { state, fatal } = useConnection();
	const live = state === "ready";
	const downSince = useRef<number | null>(null);
	const lastLiveAt = useRef<number | null>(null);
	const wasLive = useRef(false);
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (live) {
			wasLive.current = true;
			downSince.current = null;
			return;
		}
		const start = Date.now();
		if (wasLive.current) lastLiveAt.current = start;
		wasLive.current = false;
		downSince.current = start;
		setNow(start);
		let timer: ReturnType<typeof setTimeout>;
		const schedule = () => {
			const down = Date.now() - start;
			const wait = down < 2_000 ? 2_000 - down : down < 30_000 ? 30_000 - down : 60_000;
			timer = setTimeout(() => {
				setNow(Date.now());
				schedule();
			}, wait);
		};
		schedule();
		return () => clearTimeout(timer);
	}, [live]);
	return connectionStatus(state, fatal, live ? null : downSince.current, lastLiveAt.current, now);
}
```

In `BoardToolbar.tsx`, replace its own clock with `const status = useConnectionStatusText();` and delete the timers it no longer needs. `BoardToolbar`'s existing tests must pass unchanged.

Add the sentence to `src/connectionRecovery.ts`, after its import. `connectionFailure` keeps its own message until Task 27, because today's tests assert it.

```ts
// added to mobile-native/src/connectionRecovery.ts
/** Spec 14's sentence for a hub that speaks another protocol version. It
 * lives in this module, which imports no React Native, so connection code
 * and screens share one copy. */
export const INCOMPATIBLE_VERSIONS =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";
```

```tsx
// mobile-native/src/sheet/SheetStatus.tsx
// A sheet's word on the connection (spec 14): one line, no button. The app
// reconnects on its own (hubConnection.ts), so nothing here asks you to.
import { Text } from "react-native";
import { useConnectionStatusText } from "../board/connectionStatus";
import { useConnection } from "../ConnectionProvider";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { useColors } from "../ui";

/** The connection's line at the top of a sheet page; nothing while live. */
export function SheetStatus() {
	const line = useConnectionStatusText();
	const { palette } = useColors();
	if (!line) return null;
	return (
		<Text style={{ color: palette.inkMid, fontSize: 13, lineHeight: 18, textAlign: "center", paddingVertical: 6 }}>
			{line}
		</Text>
	);
}

/** What a page that has never loaded says while the connection comes up, in
 * place of a wall with a Reconnect button: spec 15's "Connecting to
 * magic-kingdom…", or spec 14's sentence when no retry can help. */
export function Connecting({ hubName }: { hubName: string }) {
	const { fatal } = useConnection();
	const { palette } = useColors();
	return (
		<Text style={{ color: palette.inkMid, fontSize: 15, lineHeight: 20, textAlign: "center", padding: 32 }}>
			{fatal ? INCOMPATIBLE_VERSIONS : `Connecting to ${hubName}…`}
		</Text>
	);
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/board/connectionStatusText.test.tsx src/sheet/SheetStatus.test.tsx src/board/BoardToolbar.test.tsx src/connection.test.ts && npm run check`
Expected: PASS. If phase 2 named the toolbar's test differently, run that file instead. `connection.test.ts` loads `connectionRecovery.ts` with no `react-native` mock, so it fails if that module ever imports a screen.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/connectionStatus.ts mobile-native/src/board/connectionStatusText.test.tsx mobile-native/src/board/BoardToolbar.tsx mobile-native/src/connectionRecovery.ts mobile-native/src/sheet/SheetStatus.tsx mobile-native/src/sheet/SheetStatus.test.tsx
git commit -m "feat(native): one connection line for the Board and the sheets"
```

### Task 3: The Hub sheet behind the hub button

**Files:**
- Create:
  - `mobile-native/src/hub/hubHeader.ts`;
  - `mobile-native/src/hub/HubSheet.tsx` (the nested stack, `HubRoutes`, the sheet's context);
  - `mobile-native/src/hub/HubHome.tsx`.
- Modify:
  - `mobile-native/src/screens.tsx`: `Routes` gains `Hub: NavigatorScreenParams<HubRoutes>`.
  - `mobile-native/App.tsx`: register `Hub` with `presentation: "modal"` and `headerShown: false`.
  - `mobile-native/src/board/BoardScreen.tsx`: the hub button opens the sheet, and phase 2's interim menu goes (phase 2 ruling 9).
- Test:
  - `mobile-native/src/hub/hubHeader.test.ts`;
  - `mobile-native/src/hub/HubHome.test.tsx`;
  - `mobile-native/src/hub/HubSheet.test.tsx`;
  - the Board's screen test, updated.

**Interfaces:**
- Consumes: Tasks 1 and 2; `useRetainedScreenConnection` (`src/retainedScreen.tsx`); `isReady` (`src/connectionDisplay.ts`); `UpdateCheckResponse` from `@evener/appwire-client`.
- Produces:
  - `hubStatusLine(connection: string | null, check: UpdateCheckResponse | null): string`.
  - `type HubRoutes = { HubHome: { hubId: string } }`. Each later page PR adds its own entry.
  - `interface HubSheetContextValue { hubId: string; hubName: string; client: AppwireClient | null; ready: boolean; canUseConnection: () => boolean }`. Later PRs add `updates` (Task 5) and `hosts` and `live` (Task 11).
  - `useHubSheet(): HubSheetContextValue`, `HubSheetProvider`, `HubSheet` (the root route's component) and `HubHome`.
  - `useClosesOnHubChange(hubId: string, close: () => void, leave: () => void)`: calls `leave` when no hub is selected, and `close` when the selected hub is no longer the one the sheet opened for.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/hub/hubHeader.test.ts
import { expect, it } from "vitest";
import type { UpdateCheckResponse } from "@evener/appwire-client";
import { hubStatusLine } from "./hubHeader";

const check = (over: Partial<UpdateCheckResponse> = {}): UpdateCheckResponse => ({
	channel: "release",
	buildChannel: "release",
	currentVersion: "0.9.412",
	currentCommit: "abc1234",
	updateAvailable: false,
	applicable: true,
	...over,
});

it.each([
	[null, null, "Connected"],
	[null, check(), "Connected · evener 0.9.412 · up to date"],
	[null, check({ updateAvailable: true, latestTag: "v0.9.413" }), "Connected · evener 0.9.412 · Update available"],
	[null, check({ applicable: false, buildChannel: "dev" }), "Connected · evener 0.9.412"],
	["Reconnecting…", check(), "Reconnecting… · evener 0.9.412 · up to date"],
	["Update needed", null, "Update needed"],
] as const)("connection %s, check %o → %s", (connection, answer, expected) => {
	expect(hubStatusLine(connection, answer)).toBe(expected);
});
```

`HubHome.test.tsx` renders `HubHome` inside `HubSheetProvider` with a fake `navigation` whose `getParent()` returns a recorder of `dispatch`, `navigate` and `goBack` calls.
- Mocks:
  - `react-native` and `expo-symbols` as in Task 1;
  - `../board/connectionStatus`, with `useConnectionStatusText` returning a variable the test sets;
  - `@react-navigation/native`, with `StackActions.replace: (name, params) => ({ type: "REPLACE", payload: { name, params } })` and `useFocusEffect: (effect) => effect()`.
- Cases:
  - It shows "Connected" and the rows Providers, Plugins, Display, Hubs and "Hub settings".
  - Providers, Plugins, Display and "Hub settings" dispatch `REPLACE` to `Providers`, `Plugins`, `TranscriptPreferences` and `HubSettings` with `{ hubId: "hub-1" }` (ruling 10).
  - Hubs calls the parent's `navigate("Hubs")`.
  - With the line set to "Reconnecting…", every row is still there and still presses.
  - With the line set to "Reconnecting…", the status line reads "Reconnecting…".
  - With the line set to "Reconnecting…", `renderedText` doesn't match `/\bReconnect\b/` (Review Focus 4).

`HubSheet.test.tsx` covers `useClosesOnHubChange` through `renderHook`, with a mutable `hubId`:
- the same hub calls neither;
- another hub calls `close` once;
- `""` (no hub selected) calls `leave` once and not `close`.

The Board's screen test changes one case: pressing the hub button calls `navigation.navigate("Hub", { screen: "HubHome", params: { hubId: "hub-1" } })`. The menu's cases go.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/hub`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/hub/hubHeader.ts
import type { UpdateCheckResponse } from "@evener/appwire-client";

/** The line under the hub's name at the top of the Hub (spec 12): the
 * connection, the running version, and whether an update is waiting.
 * `connection` is the shared connection line (null while live); `check` is
 * the last evener/update/check answer, or null before one lands. A build the
 * hub can't update itself (applicable false: a dev build) says nothing about
 * updates. */
export function hubStatusLine(connection: string | null, check: UpdateCheckResponse | null): string {
	const parts = [connection ?? "Connected"];
	if (check?.currentVersion) parts.push(`evener ${check.currentVersion}`);
	if (check?.applicable) parts.push(check.updateAvailable ? "Update available" : "up to date");
	return parts.join(" · ");
}
```

```tsx
// mobile-native/src/hub/HubSheet.tsx
// The Hub (spec 12): a large sheet from the Board's hub button that holds its
// own stack, so each page pushes inside the sheet (ruling 1). Pages read the
// hub, its client and the connection's readiness from the sheet's context.
import type { AppwireClient } from "@evener/appwire-client";
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Pressable, Text } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { isReady } from "../connectionDisplay";
import { useRetainedScreenConnection } from "../retainedScreen";
import type { Routes } from "../screens";
import { useColors } from "../ui";
import { HubHome } from "./HubHome";

/** The Hub's pages. Every page names the hub it was opened for. */
export type HubRoutes = {
	HubHome: { hubId: string };
};

export interface HubSheetContextValue {
	hubId: string;
	hubName: string;
	/** The client pages read through: the live one while ready, the last ready
	 * one while a retry dials (useRenderClient's contract, connectionDisplay.ts). */
	client: AppwireClient | null;
	ready: boolean;
	/** Whether a control that needs the hub may act right now. */
	canUseConnection: () => boolean;
}

const HubSheetContext = createContext<HubSheetContextValue | null>(null);
export const HubSheetProvider = HubSheetContext.Provider;

export function useHubSheet(): HubSheetContextValue {
	const value = useContext(HubSheetContext);
	if (!value) throw new Error("A Hub page must render inside HubSheet.");
	return value;
}

/** The Hub is always the selected hub's: a switch closes it, and removing the
 * selected hub leaves for the first-run screen. */
export function useClosesOnHubChange(hubId: string, close: () => void, leave: () => void) {
	const [openedFor] = useState(hubId);
	const handled = useRef(false);
	useEffect(() => {
		if (handled.current || hubId === openedFor) return;
		handled.current = true;
		if (hubId === "") leave();
		else close();
	}, [hubId, openedFor, close, leave]);
}

const HubStack = createNativeStackNavigator<HubRoutes>();

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile } = useConnection();
	const hubId = activeProfile?.id ?? "";
	const { state, canUseConnection, renderClient } = useRetainedScreenConnection(hubId);
	const { palette } = useColors();
	useClosesOnHubChange(
		hubId,
		() => navigation.goBack(),
		() => navigation.navigate("Hubs"),
	);
	const value = useMemo(
		() => ({
			hubId,
			hubName: activeProfile?.name ?? "",
			client: renderClient,
			ready: isReady(state),
			canUseConnection,
		}),
		[hubId, activeProfile?.name, renderClient, state, canUseConnection],
	);
	if (!activeProfile) return null;
	return (
		<HubSheetProvider value={value}>
			<HubStack.Navigator
				screenOptions={{
					headerStyle: { backgroundColor: palette.canvas },
					headerTintColor: palette.accentInk,
					headerTitleStyle: { color: palette.inkHi },
					headerShadowVisible: false,
					headerBackButtonDisplayMode: "minimal",
					contentStyle: { backgroundColor: palette.canvas },
				}}
			>
				<HubStack.Screen
					name="HubHome"
					component={HubHome}
					initialParams={{ hubId }}
					options={{
						title: activeProfile.name,
						headerRight: () => (
							<Pressable accessibilityRole="button" accessibilityLabel="Done" onPress={() => navigation.goBack()}>
								<Text style={{ color: palette.accentInk, fontSize: 17, fontWeight: "600" }}>Done</Text>
							</Pressable>
						),
					}}
				/>
			</HubStack.Navigator>
		</HubSheetProvider>
	);
}
```

```tsx
// mobile-native/src/hub/HubHome.tsx
// The Hub's first page (spec 12): the hub's status line, then one row per
// page. A row whose page hasn't landed yet leaves the sheet for today's screen
// (ruling 10); each later PR swaps its row for a push.
import { StackActions } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { Text } from "react-native";
import { useConnectionStatusText } from "../board/connectionStatus";
import { Group, GroupedPage, GroupLabel, Row } from "../sheet/Grouped";
import { useColors } from "../ui";
import { hubStatusLine } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./HubSheet";

type InterimScreen = "Providers" | "Plugins" | "TranscriptPreferences" | "HubSettings";

export function HubHome({ navigation }: NativeStackScreenProps<HubRoutes, "HubHome">) {
	const { hubId } = useHubSheet();
	const { palette } = useColors();
	const line = hubStatusLine(useConnectionStatusText(), null);
	const root = navigation.getParent();
	const leaveFor = (screen: InterimScreen) => root?.dispatch(StackActions.replace(screen, { hubId }));
	return (
		<GroupedPage>
			<Text style={{ color: palette.inkMid, fontSize: 15, lineHeight: 20, paddingHorizontal: 32, paddingTop: 4 }}>
				{line}
			</Text>
			<GroupLabel>Setup</GroupLabel>
			<Group>
				<Row icon="key" label="Providers" chevron onPress={() => leaveFor("Providers")} />
				<Row icon="puzzlepiece.extension" label="Plugins" chevron onPress={() => leaveFor("Plugins")} />
			</Group>
			<GroupLabel>This phone</GroupLabel>
			<Group>
				<Row icon="textformat.size" label="Display" chevron onPress={() => leaveFor("TranscriptPreferences")} />
				<Row
					icon="point.3.connected.trianglepath.dotted"
					label="Hubs"
					chevron
					onPress={() => root?.navigate("Hubs")}
				/>
			</Group>
			<GroupLabel>More</GroupLabel>
			<Group>
				<Row icon="info.circle" label="Hub settings" chevron onPress={() => leaveFor("HubSettings")} />
			</Group>
		</GroupedPage>
	);
}
```

Register the route in `App.tsx`: `<Stack.Screen name="Hub" component={HubSheet} options={{ presentation: "modal", headerShown: false }} />`. In `Routes`, add `Hub: NavigatorScreenParams<HubRoutes>`, importing `NavigatorScreenParams` from `@react-navigation/native` and `HubRoutes` as a type.

In `BoardScreen.tsx`, the hub button's press becomes `navigation.navigate("Hub", { screen: "HubHome", params: { hubId } })`. Delete the menu it opened ("Hub settings", "Switch hub"): Hubs is now a row in the sheet.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/hub src/board && npm run check && make test-native-bundle`
Expected: PASS. The bundle gate runs because `App.tsx` gains a route.

- [ ] **Step 5: Look in the simulator.** Build Release and open the Board. The hub button raises a large sheet titled with the hub's name. Swipe down and Done both close it, and each row opens its screen.

- [ ] **Step 6: Commit and open PR 1**

```bash
git add mobile-native/src/hub mobile-native/src/screens.tsx mobile-native/App.tsx mobile-native/src/board/BoardScreen.tsx mobile-native/src/board/BoardScreen.test.tsx
git commit -m "feat(native): the Hub sheet opens from the Board's hub button"
```

Open PR 1: "feat(native): the Hub sheet (phase 5, PR 1)". The description says the rows open today's screens until their pages land (ruling 10), and lists which PR replaces each.

---

## PR 2: the shared hub-update client, About and hub updates

### Task 4: The hub-update client moves into the package

The web checks for and applies the hub's own updates through `cmd/evener-hub/frontend/src/stores/hubUpdate.ts`. Jesse asked for one API that both clients share (ruling 22), so this task lifts that store's logic into `@evener/appwire-client` first, with the web wrapping it unchanged; Task 5 then points the phone at it.

**Files:**
- Create: `appwire-client/typescript/hubUpdate.ts` and `appwire-client/typescript/hubUpdate.test.ts`
- Modify:
  - `appwire-client/typescript/index.ts` (root exports) and the package README's module list;
  - `cmd/evener-hub/frontend/src/stores/hubUpdate.ts`: its zustand store becomes a thin wrapper over the package's controller, with the same exports and behavior.
- Test: `appwire-client/typescript/hubUpdate.test.ts`; the web's `src/stores/hubUpdate.test.ts` and `src/panes/settings/sections/hubUpdates.test.tsx` pass unchanged.

**Interfaces:**
- Consumes: `AppwireClientLike`, `UpdateCheckResponse`, `ConnectionClosedError`, `WireError` and `friendlyErrorMessage`, all the package's own.
- Produces, as root exports of `@evener/appwire-client`:
  - `type UpdateChannel = "release" | "snapshot"` and `APPLY_TIMEOUT_MS = 6 * 60_000`, moved from the web store, which re-exports both;
  - `interface HubUpdateState { channel: UpdateChannel | null; check: UpdateCheckResponse | null; checking: boolean; checkError: string | null; applying: boolean; applyError: string | null; restarting: boolean; restartTimedOut: boolean }`, the web store's state without its methods;
  - `interface HubUpdatePorts { client(): Pick<AppwireClientLike, "request">; awaitRestart(previousVersion: string | null): Promise<boolean> }`:
    - `client()` returns the connected client, or throws when there is none, so a request that was never sent reports as an error (the web's `requireClient`);
    - `awaitRestart` runs once the hub answered that it is restarting, or its answer was lost on the way, and resolves `true` once the new hub is up, or `false` when it didn't come back;
  - `interface HubUpdateController { getState(): HubUpdateState; subscribe(listener: () => void): () => void; setChannel(channel: UpdateChannel): void; runCheck(): Promise<void>; apply(): Promise<void>; dispose(): void }`
  - `createHubUpdateController(ports: HubUpdatePorts): HubUpdateController`

**Requirements (ruling 22):**
1. **The web's behavior, moved.** The controller does exactly what the web store does today (`stores/hubUpdate.ts:117-215`):
   - `setChannel` retires any check in flight and clears the result, its error, the apply error and `restartTimedOut`;
   - `runCheck` first clears the previous result, its error and a stale `restartTimedOut`, then sends `evener/update/check` with `{ channel: channel ?? "" }`, and only the newest check writes its answer or its error (`friendlyErrorMessage`);
   - `apply` does nothing while applying or restarting. It refuses without a check ("Check for updates first"), for a check from another channel ("That result is stale; run a fresh check first"), and when nothing is waiting ("Already up to date"), sending nothing. Otherwise it sends `evener/update/apply` with `{ channel: channel ?? "" }` and `{ timeoutMs: APPLY_TIMEOUT_MS }`;
   - an answer of `restarting: false` is "Already up to date";
   - a `WireError`, a `ConnectionClosedError`, or an error whose message says "cannot call" means the hub refused or never got the request, and stays an error. Any other failure means the answer was lost, and waits for the restart as a success does.
2. **Restarting.** After the hub answered (or its answer was lost), `restarting` is true until `ports.awaitRestart(check.currentVersion)` resolves: `true` clears it, and `false` clears it and sets `restartTimedOut`.
3. **No host globals.** The module uses no zustand, `fetch`, timers or `window`; the restart wait is a port, as every host API is in the package. `dispose()` stops notifying listeners.
4. **The web wraps it.** `stores/hubUpdate.ts` keeps its exports (`hubUpdateStore` with `setChannel`, `runCheck` and `apply`; `useHubUpdateStore`; `resetHubUpdateStoreForTests`; `RESTART_POLL_MS` and `RESTART_TIMEOUT_MS`), re-exports `APPLY_TIMEOUT_MS` and `UpdateChannel`, mirrors the controller's state through `subscribe`, and calls the controller from its three methods. Its ports:
   - `client` is `requireClient` (`connectedClientPort("hubUpdate")`);
   - `awaitRestart` is today's `waitForNewHub`: it polls `/api/health` every `RESTART_POLL_MS` through the injectable `fetchImpl` until the version differs, then calls the injectable `reload` and resolves `true`, or resolves `false` after `RESTART_TIMEOUT_MS`.

   `resetHubUpdateStoreForTests` builds a fresh controller over the ports it injects, so the web's tests keep driving the poll with fake timers.

- [ ] **Step 1: Write the failing tests** in `appwire-client/typescript/hubUpdate.test.ts`, over a scripted client (each request answered by the test) and a scripted `awaitRestart`. Port the web test's fixtures (`stores/hubUpdate.test.ts`) and cover:
  - `runCheck` sends `evener/update/check` with the selected channel, and `""` with none; of two overlapping checks, only the newest writes, and a late failure from a superseded one writes nothing;
  - a failed check keeps a `WireError`'s own message, and shows the generic message for anything else, clearing the previous result;
  - `setChannel` clears the result and a stale `restartTimedOut`;
  - `apply` sends nothing without a check, for another channel's check, or for an up-to-date one, with each refusal's message;
  - `apply` sends `evener/update/apply` with the channel and `{ timeoutMs: APPLY_TIMEOUT_MS }`, sets `restarting`, and calls `awaitRestart` with the check's `currentVersion`; `true` clears `restarting`, and `false` also sets `restartTimedOut`;
  - `restarting: false` in the answer reports "Already up to date" and awaits nothing;
  - a `WireError`, a `ConnectionClosedError` and a "cannot call" error stay errors and await nothing, while a plain `Error` awaits the restart;
  - a second `apply` while one is in flight sends nothing;
  - after `dispose()`, a listener hears nothing more.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/hubUpdate.test.ts`. Expected: FAIL, the module doesn't exist.
- [ ] **Step 3: Implement** the controller by moving the web store's logic, add the root exports and the README line ("the hub's own update check and apply, with the restart wait as a port"), then make the web store the wrapper above.
- [ ] **Step 4: Run them and watch them pass**, with the web's tests unchanged. Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/hubUpdate.test.ts src/stores/hubUpdate.test.ts src/panes/settings/sections/hubUpdates.test.tsx && npm run typecheck && npx biome check --write ../../../appwire-client/typescript/hubUpdate.ts ../../../appwire-client/typescript/hubUpdate.test.ts src/stores/hubUpdate.ts`, then `make test-api-package` from the repository root.
- [ ] **Step 5: Commit** (`refactor(appwire-client): one hub-update client for the web and the phone`).

### Task 5: About on the Hub's home, and the old upgrade goes

**Files:**
- Modify:
  - `mobile-native/package.json`, `package-lock.json` and `Podfile.lock`: add `expo-application`.
  - `mobile-native/src/hub/HubSheet.tsx`: the context gains `updates`.
  - `mobile-native/src/hub/HubHome.tsx`.
  - `mobile-native/src/HubSettingsScreen.tsx` (requirement 4) and `mobile-native/src/HubSettingsScreen.reconnect.test.tsx` (its upgrade cases go).
  - `mobile-native/src/location.ts` and `mobile-native/src/location.test.ts`.
- Delete (ruling 22), each once nothing imports it:
  - `src/HubUpgradeSection.tsx` and `src/HubUpgradeSection.test.tsx`;
  - `src/hubUpgrade.ts`, `src/hubUpgrade.test.ts`, `src/hubUpgradeRepository.ts`, `src/hubUpgradeRepository.test.ts`, `src/hubUpgradeValidation.ts` and `src/nativeHubUpgrade.ts`.
- Test: `mobile-native/src/hub/HubHome.test.tsx`.

**Interfaces:**
- Consumes: Task 4's `createHubUpdateController` from `@evener/appwire-client`; `nativeApplicationVersion` and `nativeBuildVersion` from `expo-application`.
- Produces: `HubSheetContextValue.updates: HubUpdateController`, created per `client` in `HubSheet` with `useMemo`, disposed when the client changes, and checked (`runCheck()`) whenever `ready` turns true for a client: on opening, and after every reconnect. Its ports:
  - `client` returns the sheet's client, and throws while there is none;
  - `awaitRestart(previousVersion)` resolves `true` once the connection is ready again and `evener/update/check` reads a `currentVersion` other than `previousVersion`. It has no timeout of its own, because while the hub is away the sheet's connection line says so (spec 14); it resolves `false` if the controller is disposed first.

**Requirements (spec 12, rulings 12 and 22):**
1. The status line passes the controller's `check` into `hubStatusLine`, so the header reads "Connected · evener 0.9.412 · up to date" or "… · Update available".
2. **ABOUT** is the last group on the home page:
   - "Evener for iPhone", valued `${nativeApplicationVersion} (${nativeBuildVersion})`: "0.1.0 (5)". With one of them missing it shows the other; with both missing, "Unknown".
   - "Update hub", an accent row. It shows only while `check?.applicable && check.updateAvailable && !restarting`, and is disabled while `!ready` or `applying`, reading "Updating…" then.
   - Tapping "Update hub" asks `Alert.alert(\`Update ${hubName}?\`, \`Install evener ${check.latestTag ?? "the latest release"} on ${hubName}. The hub restarts, and the app reconnects on its own.\`, [{ text: "Cancel", style: "cancel" }, { text: "Update", onPress: () => void updates.apply() }])`.
   - While `restarting`, a footer says `Restarting into ${check.latestTag ?? "the new release"}…`.
   - `checkError` or `applyError` shows as a danger footer, with no button: the next opening checks again.
   - About doesn't show the hub's version (spec 12: the header already does).
3. **MORE** is the last group before ABOUT, and it stays (ruling 12). In place of "Hub settings" alone, it holds three rows: "Keyboard shortcuts" (replace to `KeybindingPreferences` with `{ hubId }`), "Launch defaults" (replace to `LaunchSettings` with `{ hubId }`) and "Hub settings" (replace to `HubSettings` with `{ hubId }`).
4. **`HubSettings` stays**, for the hub's runtime, storage, agents and MCP servers (ruling 12). Its "Hub update" section goes with the old upgrade modules (About replaces it), and so do its links to Providers, Plugins, Transcript display, Keyboard shortcuts and Launch defaults: the Hub sheet has a row for each, and phase 5 retires the routes the first three open (Tasks 7, 13 and 15). Its `HubSettingsScreen.reconnect.test.tsx` keeps its overview cases, and the upgrade cases go with the section.
5. `restoredStack` (`src/location.ts`) restores a saved `keybindings` location as `KeybindingPreferences` alone, without the `HubSettings` screen that used to sit under it, since the Hub's MORE row now opens it straight from the Board. `LocationRepository.read` still accepts the saved shape, so a phone restored mid-edit still reopens the editor. Update `location.test.ts`'s keybindings case to the new stack.
6. Stored upgrade checkpoints (`evener:hub-upgrade:<hubId>`) stay where they are: nothing reads them, and deleting them isn't worth a migration.

- [ ] **Step 1: Add expo-application.** From `mobile-native`, with `[ -L node_modules ]` printing nothing, run `npx expo install expo-application`. Then regenerate the pod lock as phase 2's Task 4 Step 6 does. Expected: the lock's diff adds `EXApplication` only.
- [ ] **Step 2: Write the failing tests** in `HubHome.test.tsx`.
  - Mock `expo-application` as `{ nativeApplicationVersion: "0.1.0", nativeBuildVersion: "5" }`, and provide `updates` in the context from a real `createHubUpdateController` over a scripted client, with the sheet's ports.
  - About reads "Evener for iPhone" with "0.1.0 (5)".
  - With an up-to-date check, the status line ends "up to date", and no "Update hub" row exists.
  - With a waiting update, "Update hub" shows. Pressing it records one `Alert.alert` (the kit's `alertRequests`) titled "Update Work hub?", and invoking its "Update" button sends `evener/update/apply`.
  - After the apply answers, the row is gone and "Restarting into v0.9.413…" shows.
  - With `ready: false`, the row is disabled.
  - A failed check shows its message as a footer, and no element's text is "Retry" or matches `/\bReconnect\b/`.
  - MORE's three rows dispatch `REPLACE` to `KeybindingPreferences`, `LaunchSettings` and `HubSettings`.
- [ ] **Step 3: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/HubHome.test.tsx src/location.test.ts`
- [ ] **Step 4: Implement**, then delete the old files. Run `npm run check` and remove every import it reports as missing, with no other edits.
- [ ] **Step 5: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hub src/location.test.ts src/HubSettingsScreen.reconnect.test.tsx && npm run check && make test-native-bundle`
- [ ] **Step 6: Commit and open PR 2.**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/Podfile.lock mobile-native/src/hub mobile-native/src/HubSettingsScreen.tsx mobile-native/src/HubSettingsScreen.reconnect.test.tsx mobile-native/src/location.ts mobile-native/src/location.test.ts
git rm mobile-native/src/HubUpgradeSection.tsx mobile-native/src/HubUpgradeSection.test.tsx mobile-native/src/hubUpgrade.ts mobile-native/src/hubUpgrade.test.ts mobile-native/src/hubUpgradeRepository.ts mobile-native/src/hubUpgradeRepository.test.ts mobile-native/src/hubUpgradeValidation.ts mobile-native/src/nativeHubUpgrade.ts
git commit -m "feat(native): About and hub updates on the Hub's home"
```

Open PR 2: "feat(native): the shared hub-update client, About and hub updates (phase 5, PR 2)". The description names ruling 22, the lift the web now wraps, and what was deleted.

---

## PR 3: Display

### Task 6: The phone's display choices

Appearance and reading font belong to the phone, not a hub (spec 12's Display).
- Appearance goes through React Native's own override, `Appearance.setColorScheme`, so native chrome follows too.
  - It updates `Appearance.getColorScheme()` synchronously (`react-native/Libraries/Utilities/Appearance.js`).
  - Applying the stored choice before the first render means the first frame is already right.
- The reading font reaches components through a context whose default is the serif. `ui.tsx`, which nearly every test imports, therefore never touches native storage.

**Files:**
- Create:
  - `mobile-native/src/display/displayPreferences.ts` (pure);
  - `mobile-native/src/display/displayContext.tsx`;
  - `mobile-native/src/display/nativeDisplay.ts`.
- Modify:
  - `mobile-native/App.tsx`: apply the appearance before the first render, and provide the choices.
  - `mobile-native/src/ui.tsx`: `Copy`'s `yourMessage` role.
  - `mobile-native/src/MarkdownResponse.tsx`: the agent prose role.
  - Every other component that reads `typeRoles.` outside `src/design/`. Find them with `grep -rn "typeRoles\." mobile-native/src --include=*.tsx | grep -v src/design`; phases 2 to 4 add some.
  - `mobile-native/src/renderNative.testkit.tsx`: add `Appearance: { setColorScheme: () => {} }` to `nativeModuleMock()`.
- Test:
  - `mobile-native/src/display/displayPreferences.test.ts`;
  - `mobile-native/src/display/displayContext.test.tsx`;
  - `mobile-native/src/display/nativeDisplay.test.ts`;
  - `mobile-native/src/MarkdownResponse.test.tsx` (one case).

**Interfaces:**
- Produces:
  - `type AppearanceChoice = "system" | "light" | "dark"` and `type ReadingFont = "serif" | "sans"`;
  - `interface DisplayChoices { appearance: AppearanceChoice; readingFont: ReadingFont }`, `DEFAULT_DISPLAY` and `DISPLAY_KEY = "evener.native.display"`;
  - `class DisplayPreferences`: constructor `(storage: DisplayStorage)`, `getSnapshot()`, `subscribe(listener)` and `set(change: Partial<DisplayChoices>)`;
  - `colorSchemeFor(appearance): "light" | "dark" | "unspecified"`;
  - `DisplayProvider`, `useDisplayPreferences(): DisplayPreferences | null` and `useDisplayChoices(): DisplayChoices`;
  - `type ReadingRole = { fontFamily?: string; fontSize: number; lineHeight: number }` and `type ReadingRoles = Record<keyof typeof typeRoles, ReadingRole>`;
  - `readingRoles(font: ReadingFont): ReadingRoles` and `useReadingType(): ReadingRoles`;
  - `displayPreferences` (the app's instance) and `followAppearanceChoice(prefs?): () => void`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/display/displayPreferences.test.ts
import { expect, it } from "vitest";
import { colorSchemeFor, DEFAULT_DISPLAY, DISPLAY_KEY, DisplayPreferences } from "./displayPreferences";

function memory(stored?: string) {
	const values = new Map<string, string>();
	if (stored !== undefined) values.set(DISPLAY_KEY, stored);
	return {
		values,
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
	};
}

it("starts at the system appearance and the serif", () => {
	expect(new DisplayPreferences(memory()).getSnapshot()).toEqual({ appearance: "system", readingFont: "serif" });
});

it("keeps a choice across launches", () => {
	const storage = memory();
	new DisplayPreferences(storage).set({ appearance: "dark" });
	expect(new DisplayPreferences(storage).getSnapshot()).toEqual({ appearance: "dark", readingFont: "serif" });
});

it("reads an unknown or broken stored value as the default, one field at a time", () => {
	expect(new DisplayPreferences(memory('{"appearance":"sepia","readingFont":"sans"}')).getSnapshot()).toEqual({
		appearance: "system",
		readingFont: "sans",
	});
	expect(new DisplayPreferences(memory("{not json")).getSnapshot()).toEqual(DEFAULT_DISPLAY);
});

it("tells its listeners once per real change", () => {
	const prefs = new DisplayPreferences(memory());
	let heard = 0;
	prefs.subscribe(() => {
		heard += 1;
	});
	prefs.set({ readingFont: "sans" });
	prefs.set({ readingFont: "sans" });
	expect(heard).toBe(1);
});

it("maps each appearance to React Native's override", () => {
	expect((["system", "light", "dark"] as const).map(colorSchemeFor)).toEqual(["unspecified", "light", "dark"]);
});
```

```tsx
// mobile-native/src/display/displayContext.test.tsx
import { expect, it, vi } from "vitest";
import { typeRoles } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { DisplayProvider, type ReadingRoles, readingRoles, useReadingType } from "./displayContext";
import { DisplayPreferences } from "./displayPreferences";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("keeps the serif roles for Serif", () => {
	expect(readingRoles("serif").agentProse.fontFamily).toBe(typeRoles.agentProse.fontFamily);
});

it("drops the serif for Sans, keeps every size, and leaves Menlo alone", () => {
	const sans = readingRoles("sans");
	for (const name of Object.keys(typeRoles) as (keyof typeof typeRoles)[]) {
		expect(sans[name].fontSize).toBe(typeRoles[name].fontSize);
		expect(sans[name].lineHeight).toBe(typeRoles[name].lineHeight);
	}
	expect(sans.agentProse.fontFamily).toBeUndefined();
	expect(sans.yourMessage.fontFamily).toBeUndefined();
	expect(sans.machine.fontFamily).toBe("Menlo");
});

it("reads the phone's choice, and the serif with no provider", () => {
	const seen: { roles: ReadingRoles | null } = { roles: null };
	function Probe() {
		seen.roles = useReadingType();
		return null;
	}
	render(<Probe />);
	expect(seen.roles?.agentProse.fontFamily).toBe(typeRoles.agentProse.fontFamily);
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	prefs.set({ readingFont: "sans" });
	render(
		<DisplayProvider value={prefs}>
			<Probe />
		</DisplayProvider>,
	);
	expect(seen.roles?.agentProse.fontFamily).toBeUndefined();
});
```

```ts
// mobile-native/src/display/nativeDisplay.test.ts
import { expect, it, vi } from "vitest";
import { DisplayPreferences } from "./displayPreferences";

const setColorScheme = vi.fn();
vi.mock("react-native", () => ({ Appearance: { setColorScheme: (scheme: string) => setColorScheme(scheme) } }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));

it("applies the appearance at once, then only when it changes", async () => {
	const { followAppearanceChoice } = await import("./nativeDisplay");
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	followAppearanceChoice(prefs);
	expect(setColorScheme.mock.calls).toEqual([["unspecified"]]);
	prefs.set({ readingFont: "sans" });
	prefs.set({ appearance: "dark" });
	expect(setColorScheme.mock.calls).toEqual([["unspecified"], ["dark"]]);
});
```

Add one case to `MarkdownResponse.test.tsx`: rendered inside a `DisplayProvider` whose choice is Sans, the markdown's paragraph style has no `fontFamily`. Read the test's existing mocks first and follow them.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/display src/MarkdownResponse.test.tsx`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/display/displayPreferences.ts
// This phone's display choices (spec 12's Display): the appearance and the
// reading font. They belong to the phone, not a hub, so they survive switching
// hubs and aren't cleared when a hub is removed.
export type AppearanceChoice = "system" | "light" | "dark";
export type ReadingFont = "serif" | "sans";

export interface DisplayChoices {
	appearance: AppearanceChoice;
	readingFont: ReadingFont;
}

export const DEFAULT_DISPLAY: DisplayChoices = { appearance: "system", readingFont: "serif" };
export const DISPLAY_KEY = "evener.native.display";

export interface DisplayStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
}

const APPEARANCES: readonly string[] = ["system", "light", "dark"];
const FONTS: readonly string[] = ["serif", "sans"];

/** A stored value this build doesn't know, or can't parse, reads as the
 * default one field at a time, so one bad field never costs the other. */
function read(storage: DisplayStorage): DisplayChoices {
	let value: unknown;
	try {
		value = JSON.parse(storage.getItemSync(DISPLAY_KEY) ?? "null");
	} catch {
		return DEFAULT_DISPLAY;
	}
	const record = value !== null && typeof value === "object" ? (value as Record<string, unknown>) : {};
	return {
		appearance:
			typeof record.appearance === "string" && APPEARANCES.includes(record.appearance)
				? (record.appearance as AppearanceChoice)
				: DEFAULT_DISPLAY.appearance,
		readingFont:
			typeof record.readingFont === "string" && FONTS.includes(record.readingFont)
				? (record.readingFont as ReadingFont)
				: DEFAULT_DISPLAY.readingFont,
	};
}

export class DisplayPreferences {
	private choices: DisplayChoices;
	private readonly listeners = new Set<() => void>();

	constructor(private readonly storage: DisplayStorage) {
		this.choices = read(storage);
	}

	getSnapshot = (): DisplayChoices => this.choices;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	/** Applies a choice at once and stores it. A store that fails throws after
	 * the choice has applied, so the page can say it won't survive a restart. */
	set(change: Partial<DisplayChoices>): void {
		const next = { ...this.choices, ...change };
		if (next.appearance === this.choices.appearance && next.readingFont === this.choices.readingFont) return;
		this.choices = next;
		for (const listener of this.listeners) listener();
		this.storage.setItemSync(DISPLAY_KEY, JSON.stringify(next));
	}
}

/** React Native's override for the whole app, native chrome included:
 * "unspecified" follows the system again. */
export function colorSchemeFor(appearance: AppearanceChoice): "light" | "dark" | "unspecified" {
	return appearance === "system" ? "unspecified" : appearance;
}
```

```tsx
// mobile-native/src/display/displayContext.tsx
// The phone's display choices for components. The app's root provides the
// stored choices; a tree without a provider (a test) reads the defaults, so
// ui.tsx never has to reach native storage.
import { createContext, useContext, useSyncExternalStore } from "react";
import { fonts, typeRoles } from "../design/tokens";
import { DEFAULT_DISPLAY, type DisplayChoices, type DisplayPreferences, type ReadingFont } from "./displayPreferences";

const DisplayContext = createContext<DisplayPreferences | null>(null);
export const DisplayProvider = DisplayContext.Provider;

const noSubscription = () => () => {};
const defaultChoices = () => DEFAULT_DISPLAY;

/** The provided preferences, for a page that changes them. */
export function useDisplayPreferences(): DisplayPreferences | null {
	return useContext(DisplayContext);
}

export function useDisplayChoices(): DisplayChoices {
	const prefs = useContext(DisplayContext);
	return useSyncExternalStore(prefs?.subscribe ?? noSubscription, prefs?.getSnapshot ?? defaultChoices);
}

export type ReadingRole = { fontFamily?: string; fontSize: number; lineHeight: number };
export type ReadingRoles = Record<keyof typeof typeRoles, ReadingRole>;

/** The reading roles for a reading font (spec 12: "Reading font: Sans" swaps
 * the serif for SF Pro). Sans keeps each role's size and line height and leaves
 * the family unset, which is the system face; machine text stays Menlo. */
export function readingRoles(font: ReadingFont): ReadingRoles {
	const roles = {} as ReadingRoles;
	for (const name of Object.keys(typeRoles) as (keyof typeof typeRoles)[]) {
		const role = typeRoles[name];
		roles[name] =
			font === "sans" && role.fontFamily !== fonts.mono
				? { fontSize: role.fontSize, lineHeight: role.lineHeight }
				: role;
	}
	return roles;
}

export function useReadingType(): ReadingRoles {
	return readingRoles(useDisplayChoices().readingFont);
}
```

```ts
// mobile-native/src/display/nativeDisplay.ts
import { Storage } from "expo-sqlite/kv-store";
import { Appearance } from "react-native";
import { colorSchemeFor, DisplayPreferences } from "./displayPreferences";

/** The phone's display choices, stored with expo-sqlite's key-value store. */
export const displayPreferences = new DisplayPreferences(Storage);

/** Applies the appearance choice now, and again on every change (spec 12).
 * App.tsx calls it once, before its first render. */
export function followAppearanceChoice(prefs: DisplayPreferences = displayPreferences): () => void {
	let applied = prefs.getSnapshot().appearance;
	Appearance.setColorScheme(colorSchemeFor(applied));
	return prefs.subscribe(() => {
		const next = prefs.getSnapshot().appearance;
		if (next === applied) return;
		applied = next;
		Appearance.setColorScheme(colorSchemeFor(next));
	});
}
```

In `App.tsx`, call `followAppearanceChoice();` at module scope after the imports, with a comment saying it runs before the first render so the first frame already has the chosen appearance. Wrap `<ConnectionProvider>` in `<DisplayProvider value={displayPreferences}>`.

In `ui.tsx`'s `Copy`, read `const reading = useReadingType();` and use `reading.yourMessage` where it reads `typeRoles.yourMessage`. In `MarkdownResponse`, do the same for `typeRoles.agentProse`, and add `reading` to the `useMemo`'s dependencies. Convert every other `typeRoles.<reading role>` the grep finds the same way.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/display src/MarkdownResponse.test.tsx src/ui.test.tsx && npm run check && make test-native-bundle`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/display mobile-native/App.tsx mobile-native/src/ui.tsx mobile-native/src/MarkdownResponse.tsx mobile-native/src/MarkdownResponse.test.tsx mobile-native/src/renderNative.testkit.tsx
git commit -m "feat(native): the phone's appearance and reading font"
```

Also stage any other component the grep converted.

### Task 7: The Display page and the default detail level

**Files:**
- Create:
  - `mobile-native/src/hub/DisplayPage.tsx`;
  - `mobile-native/src/hub/DetailLevelPage.tsx`;
  - `mobile-native/src/session/detailLevels.ts`, only if phase 3's Task 12 didn't land it ("Built on earlier phases").
- Modify:
  - `mobile-native/src/hub/HubSheet.tsx`: routes `Display: { hubId }` and `DetailLevel: { hubId }`.
  - `mobile-native/src/hub/HubHome.tsx`: Display pushes, with a value.
  - `mobile-native/App.tsx` and `mobile-native/src/screens.tsx`: `TranscriptPreferences` goes.
- Delete:
  - `mobile-native/src/TranscriptPreferencesScreen.tsx`;
  - `mobile-native/src/TranscriptPreferencesEditor.tsx`, whose conflict, uncertain-write and unreadable-draft handling moves into `DetailLevelPage.tsx`;
  - `TranscriptPreferencesEditor.test.tsx`, whose cases move to `DetailLevelPage.test.tsx` and are rewritten to the new copy.
- Test: `mobile-native/src/hub/DisplayPage.test.tsx` and `mobile-native/src/hub/DetailLevelPage.test.tsx`.

**Interfaces:**
- Consumes:
  - Task 6's `useDisplayPreferences` and `useDisplayChoices`;
  - `useNativePreferences()` (`src/NativePreferencesProvider.tsx`): `model` with `editTranscript(config)`, `saveTranscript()`, `refresh()`, `discardTranscriptDraft()` and `rebaseTranscriptDraft(revision)`; `snapshot.transcriptMobile`; `connected`;
  - `CONTENT_LEVELS`, `presetContent` and `HOOK_EXIT_DETAILS` from `@evener/appwire-client`;
  - `DETAIL_LEVELS` from `src/session/detailLevels.ts` (phase 3's Task 12): `{ level, label, description }[]` in spec 8.2's order.

**Requirements (spec 12's Display, spec 8.2):**
1. **Display page** (title "Display"):
   - "APPEARANCE": `Segmented` System, Light, Dark.
   - "READING FONT": `Segmented` Serif, Sans, with the footer "For what agents write: messages, plans and documents.".
   - "DEFAULT DETAIL LEVEL": one row, "Default detail level". Its value is the hub's saved level by its `DETAIL_LEVELS` label, or "Custom". It pushes `DetailLevel`.
     - Footer: "Each session can override this from its menu."
     - A hub that doesn't offer the setting (`support === "unsupported"`) shows only the footer "This hub doesn't keep a default detail level."
   - A choice that fails to store says "This choice applies now but couldn't be saved on this phone." as a danger footer under its group.
   - No "Show model on Board rows" toggle (ruling 8).
2. **Default detail level page** (title "Default detail level"):
   - One checked row per `DETAIL_LEVELS` entry: its `label`, with its `description` as the second line.
   - Then "Custom", with the second line "Choose what the transcript shows".
   - Choosing a row edits the config and saves at once (`editTranscript` then `saveTranscript`). There is no Save button and no draft bar.
   - While saving, the rows are disabled.
   - Under Custom, "SHOWS" holds switches for the four custom fields today's editor has: action summaries, tool calls, reasoning, open details by default.
   - Under Custom, "MORE DETAIL" holds timing, token counts, estimated cost, system events and prompt events.
   - Under Custom, "HOOK EVENTS" holds three checked rows.
   - Every Custom change saves at once too.
   - Keep today's labels for these fields (`TranscriptPreferencesEditor.tsx`'s `customFields`, `advancedFields` and `hookLabels`).
3. **The states today's editor handles, calmly:**
   - Unreadable draft: a danger footer, "A saved change to this setting couldn't be read on this phone.", and one danger row, "Discard it".
   - Uncertain write: the page calls `refresh()` itself the next time the connection is ready, with the footer "Checking the hub's setting…". Its "Check current settings" button goes.
   - Conflict: the footer "The hub's setting changed while you were choosing." and two rows:
     - "Keep mine" (`rebaseTranscriptDraft(current.revision)` then `saveTranscript()`);
     - "Use the hub's" (`discardTranscriptDraft()`).
   - Load error: its message as a footer, and no button, because the preferences model reloads on the next connection.
   - `SheetStatus` sits at the top of both pages. No text anywhere says "Reconnect", "Refresh", "transcript display" or "verbosity" (spec 5).
4. HubHome's Display row pushes `Display` with `{ hubId }`, valued "System", "Light" or "Dark".

- [ ] **Step 1: Write the failing tests.** Render the pages inside `HubSheetProvider` and `DisplayProvider` over a memory-backed `DisplayPreferences`. Mock `../NativePreferencesProvider` so `useNativePreferences` returns a fake model and snapshot the test drives.
  - Choosing Dark stores `"dark"`.
  - Choosing Sans stores `"sans"`, and the footer text is present.
  - The Default detail level row reads "Intent" for a preset `intent` config and "Custom" for a custom one.
  - An unsupported hub shows the footer and no row.
  - Choosing Full calls `editTranscript` with `{ content: { kind: "preset", level: "full" } }` and then `saveTranscript`.
  - Rows are disabled while `saving`.
  - Custom shows the three groups, and flipping "Reasoning" saves.
  - A conflict shows "Keep mine" and "Use the hub's", and each calls its method.
  - An uncertain write calls `refresh()` once when `connected` turns true, and never twice for the same uncertainty.
  - An unreadable draft offers "Discard it".
  - `renderedText` never matches `/\bReconnect\b|\bRefresh\b|transcript display/i` in any of these states.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/DisplayPage.test.tsx src/hub/DetailLevelPage.test.tsx`
- [ ] **Step 3: Implement the pages, route them, and delete the old screen and editor.** `npm run check` finds what imported them.
- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hub && npm run check && make test-native-bundle`
- [ ] **Step 5: Look in the simulator.**
  - Switching to Dark turns the whole app dark at once, the sheet and its header included.
  - Relaunching keeps it.
  - Sans turns the conversation's prose and your bubbles to SF Pro.
- [ ] **Step 6: Commit and open PR 3** (`feat(native): Display in the Hub (phase 5, PR 3)`).

---

## PR 4: Hubs, pairing and first run

### Task 8: Adding a hub: scan, paste or type

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json` and `Podfile.lock` (`expo-camera`); `mobile-native/app.json` (its config plugin)
- Create: `mobile-native/src/hubs/pairing.ts`, `mobile-native/src/hubs/AddHub.tsx`
- Test: `mobile-native/src/hubs/pairing.test.ts`, `mobile-native/src/hubs/AddHub.test.tsx`

**Interfaces:**
- Consumes:
  - `parsePairingURL` (`src/connection.ts`);
  - `useConnection().saveHub(input: HubInput): Promise<boolean>`;
  - `CameraView`, `useCameraPermissions` from `expo-camera`;
  - `getStringAsync` from `expo-clipboard`.
- Produces:
  - `interface PairingTarget { origin: string; token: string }`;
  - `pairingFrom(text: string): PairingTarget | null`;
  - `suggestedHubName(origin: string): string`;
  - `AddHub(props: { how: "scan" | "paste" | "address"; onConnected(): void })`.

- [ ] **Step 1: Add expo-camera.**
  - From `mobile-native`, check `[ -L node_modules ]` first, then run `npx expo install expo-camera`.
  - Add its plugin to `app.json`'s `plugins` with `cameraPermission: "Scan the pairing code shown in Evener on your computer."`. The scanner records no audio: read the installed plugin (`node_modules/expo-camera/plugin/build/withCamera.js`) for how to leave the microphone permission out, and do that.
  - Regenerate the pod lock as phase 2's Task 4 Step 6 does. Expected: the lock's diff adds the camera pod only.
- [ ] **Step 2: Write the failing tests.**

```ts
// mobile-native/src/hubs/pairing.test.ts
import { expect, it } from "vitest";
import { pairingFrom, suggestedHubName } from "./pairing";

it("reads a pairing link, ignoring the whitespace a paste brings", () => {
	expect(pairingFrom("  https://magic-kingdom:9180/auth/abc%2Fdef \n")).toEqual({
		origin: "https://magic-kingdom:9180",
		token: "abc/def",
	});
});

it("refuses anything that isn't a pairing link", () => {
	expect(pairingFrom("https://magic-kingdom:9180/")).toBeNull();
	expect(pairingFrom("WDJB-MJHT")).toBeNull();
	expect(pairingFrom("")).toBeNull();
});

it("suggests the hub's host name, without its port, as the hub's name", () => {
	expect(suggestedHubName("https://magic-kingdom:9180")).toBe("magic-kingdom");
	expect(suggestedHubName("http://100.113.28.18:9180")).toBe("100.113.28.18");
});
```

  `AddHub.test.tsx` mocks:
  - `expo-camera` as `{ CameraView: "CameraView", useCameraPermissions: () => [permission.value, request] }`, with a mutable permission;
  - `expo-clipboard` as `{ getStringAsync: vi.fn() }`;
  - `../ConnectionProvider`'s `useConnection` to `{ saveHub }`;
  - `react-native` and `expo-symbols` as in Task 1.

  Cases:
  - **Scan, granted:** a `CameraView` renders with `barcodeScannerSettings.barcodeTypes` equal to `["qr"]`. Calling its `onBarcodeScanned({ type: "qr", data: <a valid link> })` shows "Pair with https://magic-kingdom:9180" and a name field holding "magic-kingdom". "Connect" calls `saveHub({ name: "magic-kingdom", origin, token })` and then `onConnected`.
  - **A second scan** while reviewing changes nothing.
  - **An invalid code** shows "That isn't an Evener pairing code." and keeps the camera.
  - **Permission not asked yet:** it asks once on mount.
  - **Permission denied, can't ask again:** "Camera access is off for Evener." with "Open Settings" and "Paste the link instead"; the latter switches to paste.
  - **Paste:** "Paste" fills the field from the clipboard and reviews a valid link; an invalid one shows "That isn't an Evener pairing link. Copy it again from Settings, then Mobile app, in Evener on your computer."
  - **Address:** an address and a token go to review.
  - **Refused save:** stays on review with "Couldn't save this hub. Check the name and the link, and try again."
  - **The token** never appears in `renderedText`.
- [ ] **Step 3: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hubs`
- [ ] **Step 4: Implement.**

```ts
// mobile-native/src/hubs/pairing.ts
// A hub's pairing link (Settings, then Mobile app, in Evener on a computer):
// the hub's /auth/<token> URL, as a QR code or as text (spec 15).
import { parsePairingURL } from "../connection";

export interface PairingTarget {
	origin: string;
	token: string;
}

/** A scanned or pasted pairing link, or null when the text isn't one. */
export function pairingFrom(text: string): PairingTarget | null {
	try {
		return parsePairingURL(text.trim());
	} catch {
		return null;
	}
}

/** A starting name for a newly paired hub: its host name without the port. */
export function suggestedHubName(origin: string): string {
	try {
		return new URL(origin).hostname;
	} catch {
		return origin;
	}
}
```

  `AddHub` requirements:
  1. **Scan.**
     - Ask for the camera on mount when it hasn't been asked (`permission.canAskAgain && !permission.granted`).
     - Once granted, show a square `CameraView` (12pt radius, 16pt margins) with the caption "Point the camera at the pairing code.".
     - The first valid code moves to review, and scanning stops there.
     - An invalid code changes the caption, and the same text scanned again doesn't flicker it.
     - Denied: "Camera access is off for Evener.", then rows "Open Settings" (`Linking.openSettings()`) and "Paste the link instead".
  2. **Paste.**
     - A secure text field labelled "Pairing link", and an accent row "Paste" that reads the clipboard into it.
     - Submitting a valid link moves to review.
  3. **Address.**
     - "Address" (URL keyboard, placeholder `https://hub.example.com:9180`) and "Token (optional)" (secure).
     - "Continue" moves to review. An address that isn't http(s) shows `saveHub`'s own refusal on review, as today.
  4. **Review.**
     - The group label "PAIR WITH", with the origin in Menlo as a quiet row.
     - A "Name" field prefilled with `suggestedHubName`.
     - An accent "Connect" row, disabled while saving and reading "Connecting…".
     - `saveHub` resolving `true` calls `onConnected()`. A rejection shows the refusal above as a danger footer.
  5. There is no "Reconnect" and no "Retry": a refusal says what to change.
- [ ] **Step 5: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hubs && npm run check && make test-native-bundle`
- [ ] **Step 6: Commit** (`feat(native): add a hub by scanning, pasting or typing its address`).

### Task 9: Hubs in the sheet, and the first-run screen

**Files:**
- Create:
  - `mobile-native/src/hub/HubsPage.tsx`;
  - `mobile-native/src/hub/HubDetailsPage.tsx`;
  - `mobile-native/src/hub/AddHubPage.tsx` (wraps `AddHub` for the sheet);
  - `mobile-native/src/hubs/FirstRunScreen.tsx`.
- Modify:
  - `mobile-native/src/hub/HubSheet.tsx`: routes `Hubs: undefined`, `AddHub: { how: "scan" | "paste" | "address" }` and `HubDetails: { id: string }`.
  - `mobile-native/src/hub/HubHome.tsx`: Hubs pushes, with a value.
  - `mobile-native/App.tsx`: the root route `Hubs` renders `FirstRunScreen` with `headerShown: false`.
  - `mobile-native/src/screens.tsx`: delete `HubsScreen`.
- Delete: `mobile-native/src/pairingImport.ts` and `mobile-native/src/pairingImport.test.ts`, which only `HubsScreen` used.
- Test: `mobile-native/src/hub/HubsPage.test.tsx`, `mobile-native/src/hub/HubDetailsPage.test.tsx`, `mobile-native/src/hubs/FirstRunScreen.test.tsx`.

**Requirements (spec 12's Hubs, spec 15; rulings 3 and 23):**
1. **The Hubs page.**
   - "HUBS" holds every saved hub: the name, the origin as a Menlo second line, and a check on the selected one.
   - Tapping another hub calls `selectHub(id)`. The sheet then closes on its own (`useClosesOnHubChange`) and the Board shows that hub.
   - Each row's trailing value is an `info.circle` button (44pt, labelled "Details for <name>") that pushes `HubDetails`.
   - "ADD A HUB" holds "Scan pairing code" (`qrcode.viewfinder`), "Paste pairing link" (`doc.on.clipboard`) and "Enter the address", each pushing `AddHub` with its `how`. Adding selects the new hub, so the sheet closes onto its Board.
   - Footer: "In Evener on your computer, open Settings, then Mobile app, to show a pairing code."
2. **A hub's details** (titled with its name):
   - "Name", which opens today's `HubEditor` (name, and replacing the token);
   - "Address", the origin in Menlo, not tappable;
   - "Remove this hub" (danger). It asks `Alert.alert(\`Remove ${name}?\`, "The saved hub, its token and its drafts are removed from this phone.", [Cancel, Remove (destructive)])`, then calls `removeHub(id)`.
     - Removing another hub pops back to Hubs.
     - Removing the selected hub leaves navigation to the sheet: with no hub selected, it leaves for the first-run screen (Task 3's hook).
     - A failure shows its message as a danger footer.
3. **The first-run screen** (spec 15), the root `Hubs` route:
   - The title "Connect to your hub" (SF Pro semibold 28/34), then "In Evener on your computer, open Settings, then Mobile app." in `inkMid`.
   - A group with "Scan pairing code", "Paste pairing link" and a quiet "Enter the address". Choosing one shows `AddHub` in place, with a "Back" row returning to the choices, and `onConnected` navigates to `Sessions`.
   - When hubs are saved but none is selected (after removing the selected one), "SAVED HUBS" lists them. Tapping one calls `selectHub(id)` and navigates to `Sessions`.
4. The Hub home's Hubs row pushes `Hubs`, valued with the number of saved hubs.

- [ ] **Step 1: Write the failing tests.** Mock `../ConnectionProvider` with `profiles`, `activeProfile`, `selectHub`, `removeHub` and `updateHub` spies. Cover every requirement above, and assert `renderedText` never matches `/\bReconnect\b/`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/HubsPage.test.tsx src/hub/HubDetailsPage.test.tsx src/hubs/FirstRunScreen.test.tsx`
- [ ] **Step 3: Implement. Delete `HubsScreen` and `pairingImport.ts`** and fix what `npm run check` reports.
- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hub src/hubs && npm run check && make test-native-bundle`
- [ ] **Step 5: Look in the simulator.**
  - With no hub saved (fresh install), the first-run screen shows.
  - Pairing with a demo hub's link lands on the Board.
  - From the Hub sheet, add a second hub and switch between them.
  - Remove the selected one and land back on first run.
- [ ] **Step 6: Commit and open PR 4** (`feat(native): Hubs, pairing and first run (phase 5, PR 4)`).

---

## PR 5: Hosts

PR 5 starts once PRs 1 and 2 are on main: a host's version tag compares against the hub's version from Task 5's update check.

### Task 10: Host states, the hosts controller and live counts

**Files:**
- Create: `mobile-native/src/hosts/hostStatus.ts`, `mobile-native/src/hosts/hostsController.ts`, `mobile-native/src/hosts/liveCounts.ts`
- Test: `mobile-native/src/hosts/hostStatus.test.ts`, `mobile-native/src/hosts/hostsController.test.ts`, `mobile-native/src/hosts/liveCounts.test.ts`

**Interfaces:**
- Consumes: `HostRow`, `HostEntry`, `NavigationSessionSummary`, `friendlyErrorMessage` from `@evener/appwire-client`; `NavigationPages` (`src/navigationPages.ts`: constructor `(client, params, field, key, limit)`, `refresh()`, `more()`, `watch()`, `getSnapshot()`, `subscribe()`, `cancel()`).
- Produces:
  - `hostStatus(row: HostRow, connecting: boolean): HostStatus`, where `HostStatus = { word; tone: "ink" | "attention"; canConnect: boolean; footer: string | null }`;
  - `versionDriftTag(row, hubVersion): string | null`, `systemLabel(row): string | null`, `VERSION_DRIFT_FOOTER` and `HUB_TOML_FOOTER`;
  - `HOST_POLL_MS`, `HOST_GATE_TIMEOUT_MS`, `HostsState`, and `class HostsController` with `start(): () => void`, `read()`, `connect(name)`, `update(name, entry)`, `remove(name)`, `getSnapshot()`, `subscribe()` and `dispose()`;
  - `liveCountsByHost(rows)`, `liveSessionsText(count, offline)`, `LIVE_PAGE_LIMIT`, and `class LiveSessionsReader` with `load()`, `getSnapshot()`, `subscribe()` and `dispose()`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/hosts/hostStatus.test.ts
import { describe, expect, it } from "vitest";
import type { HostRow } from "@evener/appwire-client";
import { hostStatus, systemLabel, versionDriftTag } from "./hostStatus";

const row = (over: Partial<HostRow> = {}): HostRow => ({
	name: "paradise-park",
	origin: "hub.toml",
	attached: false,
	midAttach: false,
	removed: false,
	...over,
});

describe("a host's state (ruling 4)", () => {
	it.each([
		[{ attached: true }, false, "Connected", "ink", false],
		[{}, true, "Connecting…", "ink", false],
		[{ midAttach: true }, false, "Offline · reconnecting", "attention", false],
		[{}, false, "Offline", "attention", true],
		[{ lastAttachError: "ssh: connect to host paradise-park port 22: Connection refused" }, false, "Offline", "attention", true],
		[{ removed: true }, false, "Offline", "attention", false],
	] as const)("%o, connecting %s → %s", (over, connecting, word, tone, canConnect) => {
		expect(hostStatus(row(over), connecting)).toMatchObject({ word, tone, canConnect });
	});

	it("says what an offline host's footer says (spec 12)", () => {
		expect(hostStatus(row({ midAttach: true }), false).footer).toBe(
			"This host is offline, so its sessions can't be reached. The hub keeps trying to reach it.",
		);
		expect(hostStatus(row(), false).footer).toBe(
			"This host is offline, so its sessions can't be reached. Connect to reach them.",
		);
		expect(hostStatus(row({ attached: true }), false).footer).toBeNull();
	});
});

describe("version drift and the system line", () => {
	it("tags a host on another version, from its last-known version too", () => {
		expect(versionDriftTag(row({ attached: true, hubVersion: "0.9.409" }), "0.9.412")).toBe("Hub runs 0.9.412");
		expect(versionDriftTag(row({ hubVersion: "0.9.409" }), "0.9.412")).toBe("Hub runs 0.9.412");
		expect(versionDriftTag(row({ attached: true, hubVersion: "0.9.412" }), "0.9.412")).toBeNull();
		expect(versionDriftTag(row({ attached: true }), "0.9.412")).toBeNull();
		expect(versionDriftTag(row({ hubVersion: "0.9.409" }), undefined)).toBeNull();
	});

	it("names the system in words", () => {
		expect(systemLabel({ os: "darwin", arch: "arm64" })).toBe("macOS · arm64");
		expect(systemLabel({ os: "linux", arch: "amd64" })).toBe("Linux · amd64");
		expect(systemLabel({ os: "freebsd" })).toBe("freebsd");
		expect(systemLabel({})).toBeNull();
	});
});
```

```ts
// mobile-native/src/hosts/hostsController.test.ts
import { afterEach, describe, expect, it, vi } from "vitest";
import { type HostRow, WireError } from "@evener/appwire-client";
import { HOST_GATE_TIMEOUT_MS, HOST_POLL_MS, HostsController } from "./hostsController";

const row = (name: string, over: Partial<HostRow> = {}): HostRow => ({
	name,
	origin: "sidecar",
	attached: false,
	midAttach: false,
	removed: false,
	...over,
});

/** A hub that holds every request until the test settles it. */
function hub() {
	const pending: {
		method: string;
		params: unknown;
		opts?: { timeoutMs?: number };
		resolve: (value: unknown) => void;
		reject: (error: unknown) => void;
	}[] = [];
	const client = {
		request: (method: string, params: unknown, opts?: { timeoutMs?: number }) =>
			new Promise((resolve, reject) => pending.push({ method, params, opts, resolve, reject })),
	};
	const take = (method: string) => {
		const index = pending.findIndex((request) => request.method === method);
		if (index < 0) throw new Error(`no pending ${method}`);
		return pending.splice(index, 1)[0] as (typeof pending)[number];
	};
	const count = (method: string) => pending.filter((request) => request.method === method).length;
	return { client: client as never, pending, take, count };
}
const settle = () => new Promise((resolve) => setImmediate(resolve));

afterEach(() => vi.useRealTimers());

describe("the hosts controller", () => {
	it("reads the hub's hosts and leaves removed ones out", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const reading = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park"), row("gone", { removed: true })] });
		await reading;
		expect(hosts.getSnapshot().rows?.map((host) => host.name)).toEqual(["paradise-park"]);
	});

	it("keeps its rows when a read fails, and says why", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const first = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await first;
		const second = hosts.read();
		h.take("evener/host/list").reject(new WireError("hub is busy", -32000));
		await second;
		expect(hosts.getSnapshot()).toMatchObject({ error: "hub is busy" });
		expect(hosts.getSnapshot().rows).toHaveLength(1);
	});

	it("runs a read asked for mid-flight once more after it, and only once", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const first = hosts.read();
		const second = hosts.read();
		const third = hosts.read();
		h.take("evener/host/list").resolve({ hosts: [] });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await Promise.all([first, second, third]);
		expect(h.count("evener/host/list")).toBe(0);
		expect(hosts.getSnapshot().rows).toHaveLength(1);
	});

	it("polls while started, and stops only with the last stop", async () => {
		vi.useFakeTimers();
		const lists: string[] = [];
		const hosts = new HostsController({
			request: async (method: string) => {
				lists.push(method);
				return { hosts: [] };
			},
		} as never);
		const stopOne = hosts.start();
		const stopTwo = hosts.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(lists).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS);
		expect(lists).toHaveLength(2);
		stopOne();
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS);
		expect(lists).toHaveLength(3);
		stopTwo();
		await vi.advanceTimersByTimeAsync(HOST_POLL_MS * 5);
		expect(lists).toHaveLength(3);
		expect(vi.getTimerCount()).toBe(0);
	});

	it("marks a host connecting until the hub answers, then re-reads", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const connecting = hosts.connect("paradise-park");
		expect([...hosts.getSnapshot().connecting]).toEqual(["paradise-park"]);
		const attach = h.take("evener/host/attach");
		expect(attach.params).toEqual({ host: "paradise-park" });
		expect(attach.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		attach.resolve({ attached: true, host: "paradise-park" });
		await settle();
		expect(hosts.getSnapshot().connecting.size).toBe(0);
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park", { attached: true })] });
		await connecting;
		expect(hosts.getSnapshot().rows?.[0]?.attached).toBe(true);
	});

	it("keeps a refused Connect's message for that host until its next Connect", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const refused = hosts.connect("paradise-park");
		h.take("evener/host/attach").reject(new WireError("ssh: connect to host paradise-park port 22: Connection refused", -32000));
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await refused;
		expect(hosts.getSnapshot().connectErrors.get("paradise-park")).toBe(
			"ssh: connect to host paradise-park port 22: Connection refused",
		);
		void hosts.connect("paradise-park");
		expect(hosts.getSnapshot().connectErrors.has("paradise-park")).toBe(false);
	});

	it("edits and removes a host with the gate's long deadline, re-reading after each", async () => {
		const h = hub();
		const hosts = new HostsController(h.client);
		const entry = { address: "jesse@paradise-park", roots: ["/Users/jesse/git"] };
		const editing = hosts.update("paradise-park", entry);
		const edit = h.take("evener/host/update");
		expect(edit.params).toEqual({ name: "paradise-park", entry });
		expect(edit.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		edit.resolve({ host: row("paradise-park") });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [row("paradise-park")] });
		await editing;
		const removing = hosts.remove("paradise-park");
		const removal = h.take("evener/host/remove");
		expect(removal.params).toEqual({ name: "paradise-park" });
		expect(removal.opts).toEqual({ timeoutMs: HOST_GATE_TIMEOUT_MS });
		removal.resolve({ host: row("paradise-park", { removed: true }) });
		await settle();
		h.take("evener/host/list").resolve({ hosts: [] });
		await removing;
		expect(hosts.getSnapshot().rows).toEqual([]);
	});
});
```

```ts
// mobile-native/src/hosts/liveCounts.test.ts
import { expect, it } from "vitest";
import type { NavigationReadParams } from "@evener/appwire-client";
import { wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { LIVE_PAGE_LIMIT, LiveSessionsReader, liveCountsByHost, liveSessionsText } from "./liveCounts";

const session = (ref: string, host_id: string) => ({
	ref,
	host_id,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "active",
	kind: "session",
	live: true,
	children: [],
});

/** Answers the nth Live read with page(n). */
function liveHub(page: (read: number) => { sessions: ReturnType<typeof session>[]; remaining: number }) {
	const reads: NavigationReadParams[] = [];
	const client = {
		request: async (_method: string, params: NavigationReadParams) => {
			const answer = page(reads.length);
			reads.push(params);
			return wireV2(
				{ ...params, representationVersion: 2, offset: params.offset ?? 0, limit: params.limit ?? 50 },
				answer,
				`etag-${reads.length}`,
				1,
				"generation-test",
			);
		},
		onNotification: () => () => {},
	} as unknown as ConversationClientLike;
	return { client, reads };
}

it("counts live sessions by host", () => {
	expect(liveCountsByHost([{ host_id: "local" }, { host_id: "paradise-park" }, { host_id: "paradise-park" }])).toEqual(
		new Map([
			["local", 1],
			["paradise-park", 2],
		]),
	);
});

it("words a host's live sessions (spec 12)", () => {
	expect(liveSessionsText(3, false)).toBe("3 live");
	expect(liveSessionsText(3, true)).toBe("3 live, out of reach");
	expect(liveSessionsText(0, false)).toBe("No live sessions");
});

it("reads every Live page before counting", async () => {
	const { client, reads } = liveHub((read) =>
		read === 0
			? { sessions: [session("a", "local"), session("b", "paradise-park")], remaining: 1 }
			: { sessions: [session("c", "paradise-park")], remaining: 0 },
	);
	const reader = new LiveSessionsReader(client);
	await reader.load();
	expect(reads).toHaveLength(2);
	expect(liveCountsByHost(reader.getSnapshot().rows)).toEqual(
		new Map([
			["local", 1],
			["paradise-park", 2],
		]),
	);
});

it("stops after ten pages", async () => {
	const { client, reads } = liveHub((read) => ({ sessions: [session(`s${read}`, "local")], remaining: 1 }));
	await new LiveSessionsReader(client).load();
	expect(reads).toHaveLength(LIVE_PAGE_LIMIT);
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/hosts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/hosts/hostStatus.ts
// A host's state in words (spec 12; ruling 4), from the row evener/host/list
// serves. `midAttach` stays set while the hub's supervisor retries a dropped
// host (observe, cmd/evener-hub/app_host_manage.go), so a host the hub is
// already reaching for offers no Connect.
import type { HostRow } from "@evener/appwire-client";

export interface HostStatus {
	word: "Connected" | "Connecting…" | "Offline · reconnecting" | "Offline";
	/** Offline states take the attention ink: a human may be needed. */
	tone: "ink" | "attention";
	/** Connect shows only for a host the hub isn't attached to or retrying. */
	canConnect: boolean;
	/** The detail page's footer for an offline host, or null. */
	footer: string | null;
}

const UNREACHABLE = "This host is offline, so its sessions can't be reached.";

export function hostStatus(row: HostRow, connecting: boolean): HostStatus {
	if (row.attached) return { word: "Connected", tone: "ink", canConnect: false, footer: null };
	if (connecting) return { word: "Connecting…", tone: "ink", canConnect: false, footer: null };
	if (row.midAttach)
		return {
			word: "Offline · reconnecting",
			tone: "attention",
			canConnect: false,
			footer: `${UNREACHABLE} The hub keeps trying to reach it.`,
		};
	return { word: "Offline", tone: "attention", canConnect: !row.removed, footer: `${UNREACHABLE} Connect to reach them.` };
}

/** The gray tag for a host on another Evener version than the hub, from its
 * last-known version (spec 12: "Hub runs 0.9.412"); null when they match or
 * either is unknown. */
export function versionDriftTag(row: Pick<HostRow, "hubVersion">, hubVersion: string | undefined): string | null {
	return row.hubVersion && hubVersion && row.hubVersion !== hubVersion ? `Hub runs ${hubVersion}` : null;
}

/** Spec 12's footer for a connected host on another version. */
export const VERSION_DRIFT_FOOTER =
	"This host runs a different version of Evener than the hub. Sessions keep working. Update Evener on the host when it's convenient.";

/** A host from hub.toml is read-only on the phone, and says so (spec 12). */
export const HUB_TOML_FOOTER = "This host is defined in hub.toml, so it can only be edited there.";

const OS_NAMES: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

/** "macOS · arm64" from the host's last-known facts; null when unknown. */
export function systemLabel(row: Pick<HostRow, "os" | "arch">): string | null {
	const parts = [row.os ? (OS_NAMES[row.os] ?? row.os) : undefined, row.arch].filter(
		(part): part is string => !!part,
	);
	return parts.length > 0 ? parts.join(" · ") : null;
}
```

```ts
// mobile-native/src/hosts/hostsController.ts
// The hub's hosts as evener/host/list reports them, kept current while a page
// shows them. No host lifecycle notification exists, so a started controller
// reads every 2 seconds, as the web's hosts section does (HOST_POLL_MS,
// cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx), and keeps its
// last rows when a read fails.
import { friendlyErrorMessage, type HostEntry, type HostRow } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

export const HOST_POLL_MS = 2_000;
/** Connect, edit and remove wait on that host's gate, which the hub's
 * supervisor may hold for a whole reconnect cycle, well past the client's
 * 30-second default. The web allows them 35 minutes (HOST_GATE_TIMEOUT_MS,
 * cmd/evener-hub/frontend/src/stores/hosts.ts), so a slow attach isn't
 * reported as a failure while the hub is still working on it. */
export const HOST_GATE_TIMEOUT_MS = 35 * 60_000;

export interface HostsState {
	/** null until the first read lands; removed hosts are left out. */
	rows: HostRow[] | null;
	/** The last failed read's message; the rows stay as they were. */
	error: string | null;
	/** Hosts this phone asked to connect, until the hub answers. */
	connecting: ReadonlySet<string>;
	/** A refused Connect's message, per host, until its next Connect. */
	connectErrors: ReadonlyMap<string, string>;
}

export class HostsController {
	private state: HostsState = { rows: null, error: null, connecting: new Set(), connectErrors: new Map() };
	private readonly listeners = new Set<() => void>();
	private inFlight: Promise<void> | null = null;
	private again = false;
	private starts = 0;
	private loop = 0;
	private timer: ReturnType<typeof setTimeout> | null = null;
	private disposed = false;

	constructor(
		private readonly client: Pick<ConversationClientLike, "request">,
		private readonly pollMs = HOST_POLL_MS,
	) {}

	getSnapshot = (): HostsState => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	private publish(change: Partial<HostsState>) {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}

	/** Reads now, then every pollMs, until every start's stop has run. Pages
	 * start it when they come into view and stop it when they leave. */
	start(): () => void {
		this.starts += 1;
		if (this.starts === 1) void this.poll(++this.loop);
		let stopped = false;
		return () => {
			if (stopped) return;
			stopped = true;
			this.starts -= 1;
			if (this.starts > 0) return;
			this.loop += 1;
			if (this.timer) clearTimeout(this.timer);
			this.timer = null;
		};
	}

	private async poll(loop: number): Promise<void> {
		await this.read();
		if (loop !== this.loop || this.disposed) return;
		this.timer = setTimeout(() => void this.poll(loop), this.pollMs);
	}

	/** One read. A read asked for while another is in flight runs once more
	 * after it, so no answer is older than the request that asked for it. */
	read(): Promise<void> {
		if (this.inFlight) {
			this.again = true;
			return this.inFlight;
		}
		this.inFlight = (async () => {
			do {
				this.again = false;
				try {
					const { hosts } = await this.client.request("evener/host/list", {});
					this.publish({ rows: hosts.filter((row) => !row.removed), error: null });
				} catch (error) {
					this.publish({ error: friendlyErrorMessage(error) });
				}
			} while (this.again && !this.disposed);
		})().finally(() => {
			this.inFlight = null;
		});
		return this.inFlight;
	}

	/** Asks the hub to attach a host (evener/host/attach, the web's Connect),
	 * then re-reads the rows. */
	async connect(name: string): Promise<void> {
		if (this.state.connecting.has(name)) return;
		const connectErrors = new Map(this.state.connectErrors);
		connectErrors.delete(name);
		this.publish({ connecting: new Set([...this.state.connecting, name]), connectErrors });
		try {
			await this.client.request("evener/host/attach", { host: name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
		} catch (error) {
			this.publish({ connectErrors: new Map([...this.state.connectErrors, [name, friendlyErrorMessage(error)]]) });
		} finally {
			this.publish({ connecting: new Set([...this.state.connecting].filter((host) => host !== name)) });
		}
		await this.read();
	}

	async update(name: string, entry: HostEntry): Promise<HostRow> {
		const { host } = await this.client.request("evener/host/update", { name, entry }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
		await this.read();
		return host;
	}

	async remove(name: string): Promise<void> {
		await this.client.request("evener/host/remove", { name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
		await this.read();
	}

	dispose(): void {
		this.disposed = true;
		this.loop += 1;
		if (this.timer) clearTimeout(this.timer);
		this.timer = null;
		this.listeners.clear();
	}
}
```

```ts
// mobile-native/src/hosts/liveCounts.ts
// How many live sessions each host has (spec 12's "3 live"). No hosts catalog
// exists, so this reads the Live section's pages and counts rows by host
// (ruling 5). The hub keeps an offline host's last rows, so an offline host's
// count is what "3 live, out of reach" reports.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { NavigationPages } from "../navigationPages";

export function liveCountsByHost(rows: readonly Pick<NavigationSessionSummary, "host_id">[]): ReadonlyMap<string, number> {
	const counts = new Map<string, number>();
	for (const row of rows) counts.set(row.host_id, (counts.get(row.host_id) ?? 0) + 1);
	return counts;
}

export function liveSessionsText(count: number, offline: boolean): string {
	if (count === 0) return "No live sessions";
	return offline ? `${count} live, out of reach` : `${count} live`;
}

/** At most this many Live pages are read: 500 sessions, far past the fleets
 * the spec sizes for (section 2's peak is 16 top-level sessions). */
export const LIVE_PAGE_LIMIT = 10;

export class LiveSessionsReader {
	private readonly pages: NavigationPages<NavigationSessionSummary>;
	private readonly unwatch: () => void;

	constructor(client: ConversationClientLike) {
		this.pages = new NavigationPages<NavigationSessionSummary>(
			client,
			{ resource: "section", section: "live" },
			"sessions",
			(row) => row.ref,
			50,
		);
		this.unwatch = this.pages.watch();
	}

	getSnapshot = () => this.pages.getSnapshot();

	subscribe = (listener: () => void) => this.pages.subscribe(listener);

	/** Reads the first page, then the rest, up to LIVE_PAGE_LIMIT pages. */
	async load(): Promise<void> {
		await this.pages.refresh();
		for (let read = 1; read < LIVE_PAGE_LIMIT && this.pages.getSnapshot().remaining > 0; read++) await this.pages.more();
	}

	dispose(): void {
		this.unwatch();
		this.pages.cancel();
	}
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/hosts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/hosts
git commit -m "feat(native): host states, the hosts controller and live counts per host"
```

### Task 11: Hosts in the Hub

**Files:**
- Create:
  - `mobile-native/src/hub/HostsPage.tsx`, `mobile-native/src/hub/HostDetailPage.tsx` and `mobile-native/src/hub/HostEditPage.tsx`;
  - `appwire-client/typescript/hostRoots.ts`, with its test.
- Modify:
  - `mobile-native/src/hub/HubSheet.tsx`: routes, and the context gains `hosts` and `live`.
  - `mobile-native/src/hub/HubHome.tsx`: a FLEET group.
  - `appwire-client/typescript/index.ts`: export `rootsFromText` and `rootsToText`.
  - `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx`: import them instead of its private copies.
  - The Board's notices (phase 2's Task 14): a host's "Details".
- Test:
  - `mobile-native/src/hub/HostsPage.test.tsx`, `HostDetailPage.test.tsx` and `HostEditPage.test.tsx`;
  - `appwire-client/typescript/hostRoots.test.ts`.

**Interfaces:**
- Consumes:
  - Task 10;
  - `hostFieldError` and `friendlyErrorMessage` from `@evener/appwire-client`;
  - the context's `updates` (Task 5): the hub's version is `check?.currentVersion`.
- Produces:
  - `HubRoutes` gains `Hosts: { hubId: string; focus?: string }`, `HostDetail: { hubId: string; name: string }` and `HostEdit: { hubId: string; name: string }`.
  - `HubSheetContextValue` gains `hosts: HostsController` and `live: LiveSessionsReader`, created per `client` and disposed when it changes.
  - `rootsFromText(text: string): string[]` and `rootsToText(roots?: readonly string[]): string`, moved as they are from the web's `hosts.tsx`: one root per line, trimmed, blank lines dropped.

**Requirements (spec 12's Hosts; rulings 3, 4 and 5):**
1. **Hosts page** (title "Hosts"):
   - `hosts.start()` runs while the page is focused (`useFocusEffect`), and `live.load()` runs on focus.
   - The first row is the hub's own machine, quiet and not tappable:
     - labelled with the hub's name (ruling 3);
     - the second line is "Connected · <live text>" while the phone's connection is live, or the connection line otherwise;
     - the value is the hub's version.
   - Then one row per `HostRow`, in the hub's order:
     - icon `server.rack`, and the host's name;
     - the second line joins the status word, `systemLabel` and `liveSessionsText(count, !attached)`, leaving out what's unknown;
     - the value is the host's last-known version (`row.hubVersion`, in `inkMid` with tabular figures) when the hub knows it, followed by the gray `Tag` from `versionDriftTag` when there is one. Spec 12 has each row show its version, with that tag when it differs from the hub's.
     - The state stays in words on the second line, so a row carries no Offline tag. The home's amber "N offline" and the detail's status carry the attention;
     - a chevron, pushing `HostDetail`.
   - Footer: "Hosts come from hub.toml or were added in the web app. Add hosts from the web app; they need an SSH address and a key."
   - Before the first read, it shows `Connecting`; after it, the last rows stay through a failed read or a dropped connection.
   - With `focus`, it pushes that host's detail once on mount and clears the param.
2. **Host detail** (titled with the host's name; `hosts.start()` while focused):
   - The first group holds:
     - "Status": the word, in the attention ink when the tone says so;
     - "Version": the last-known version, with the drift tag;
     - "System";
     - "Sessions": `liveSessionsText`;
     - "Project roots": each root in Menlo, or "None";
     - "Defined in": "hub.toml", or "the app or web" for `sidecar`.
   - "LAST ERROR" holds `lastAttachError` in Menlo, danger ink, while the host isn't attached.
   - The actions group holds:
     - "Connect" (accent) when `canConnect`, reading "Connecting…" and disabled while in flight;
     - for `sidecar` hosts, "Edit" (accent, pushes `HostEdit`) and "Remove" (danger). Remove confirms `Alert.alert(\`Remove ${name}?\`, "The hub forgets this host. Add it again from the web app.", [Cancel, Remove (destructive)])` and pops back when done.
   - The footer is the status's footer. A connected host with drift shows `VERSION_DRIFT_FOOTER` instead. A `hub.toml` host adds `HUB_TOML_FOOTER`.
   - A refused Connect shows its `connectErrors` message as a danger footer.
   - Every action is disabled while `!ready`, and there is no force-retry for an attached host (spec 12).
   - When the host leaves the rows (removed elsewhere), the page pops back.
3. **Host edit** (title "Edit <name>", header Cancel and Save):
   - Fields with the web dialog's labels and help lines, prefilled from the row: "SSH address", "User", "Key path", "Evener path", "Hub config path", "Hub address", and "Roots" (multiline, `rootsToText`).
   - Save sends `hosts.update(name, entry)` with every field trimmed and `roots: rootsFromText(text)`, then pops.
   - A refusal blaming a field (`hostFieldError`) puts `friendlyErrorMessage` under that field in danger ink. Any other refusal goes above the fields.
   - Save is disabled only while saving: the hub is the one validator, as on the web.
4. **The Hub's home** gains "FLEET" first. It holds "Hosts" (`server.rack`), valued with the number of hosts, the hub's own machine included, plus a tag:
   - an amber "N offline" when any host is offline;
   - otherwise a gray "N on another version" when any drifts.

   The home reads the rows once on focus (`hosts.read()`) and doesn't poll.
5. **The Board's host notice:** its "Details" action becomes `navigation.navigate("Hub", { screen: "Hosts", params: { hubId, focus: hostName }, initial: false })` (ruling 25). Its phase 2 test follows.

- [ ] **Step 1: Write the failing tests.**
  - `hostRoots.test.ts`: blank lines and surrounding spaces go, and order is kept.
  - The pages: drive a scripted client answering `evener/host/list`, `evener/navigation/read` for Live (with `wireV2`), `evener/host/attach`, `evener/host/update` and `evener/host/remove`. Cover:
    - every requirement above, including `focus`;
    - the "Offline · reconnecting" host showing no Connect;
    - a drifting host's row showing its own version ("0.9.409") beside the gray "Hub runs 0.9.412" tag, a matching host's row showing its version and no tag, and a host whose version the hub doesn't know showing neither;
    - Connect's in-flight state and a refusal;
    - a `hub.toml` host with no Edit or Remove;
    - Edit's field-blamed refusal (a `WireError` whose data carries `evenerErrorInfo: "invalidHostField"` and `field: "roots"`, the shape `appwire-client/typescript/errors.test.ts` builds);
    - no `/\bReconnect\b/` anywhere.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/HostsPage.test.tsx src/hub/HostDetailPage.test.tsx src/hub/HostEditPage.test.tsx`
- [ ] **Step 3: Implement.** Move `rootsFromText` and `rootsToText` into the package unchanged, import them in the web's `hosts.tsx`, then build the pages. Run `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/hostRoots.ts ../../../appwire-client/typescript/hostRoots.test.ts ../../../appwire-client/typescript/index.ts src/panes/settings/sections/hosts.tsx`.
- [ ] **Step 4: Run them and watch them pass.** Run:
  - `cd mobile-native && npx vitest run src/hub src/board && npm run check`
  - `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/hostRoots.test.ts src/panes/settings/sections/hosts.test.tsx`
- [ ] **Step 5: Look in the simulator** against a hub with a remote host.
  - Stop the remote's SSH: the row turns "Offline · reconnecting" within a few seconds.
  - Remove the host from the web: the phone's row goes.
- [ ] **Step 6: Commit and open PR 5** (`feat(native): Hosts in the Hub (phase 5, PR 5)`).

---

## PR 6: Providers

### Task 12: Provider states

**Files:**
- Create: `mobile-native/src/providers/providerStatus.ts`
- Test: `mobile-native/src/providers/providerStatus.test.ts`

**Interfaces:**
- Produces:
  - `providerStatus(instance, auth): ProviderStatus`, where `ProviderStatus = { word; tone: "ink" | "attention" }`;
  - `authByProvider(statuses): ReadonlyMap<string, AuthStatusResponse>`;
  - `signInKind(instance): "Account" | "API key" | "None"`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/providers/providerStatus.test.ts
import { expect, it } from "vitest";
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import { authByProvider, providerStatus, signInKind } from "./providerStatus";

type Facts = Pick<InstanceEntry, "activeSource" | "authModes" | "credentialRequired">;
const instance = (over: Partial<Facts>): Facts => ({ activeSource: "none", authModes: [], credentialRequired: true, ...over });

const cases: [Partial<Facts>, Pick<AuthStatusResponse, "needsLogin"> | undefined, string, string][] = [
	[{ activeSource: "oauth", authModes: ["oauth"] }, { needsLogin: true }, "Sign-in expired", "attention"],
	[{ activeSource: "oauth", authModes: ["oauth"] }, { needsLogin: false }, "Signed in", "ink"],
	[{ activeSource: "oauth", authModes: ["oauth"] }, undefined, "Signed in", "ink"],
	[{ activeSource: "store", authModes: ["apiKey"] }, undefined, "Key set", "ink"],
	[{ activeSource: "env:LUNAROUTE_API_KEY", authModes: ["apiKey"] }, undefined, "Key set", "ink"],
	[{ activeSource: "api_key" }, undefined, "Key set", "ink"],
	[{ activeSource: "adc", authModes: ["credentialJson"] }, undefined, "Key set", "ink"],
	[{ activeSource: "none", authModes: ["oauth"] }, undefined, "Not signed in", "ink"],
	[{ activeSource: "none", authModes: ["apiKey"] }, undefined, "No key", "ink"],
	[{ activeSource: "none", credentialRequired: false }, undefined, "No sign-in needed", "ink"],
];

it.each(cases)("%o with %o → %s", (over, auth, word, tone) => {
	expect(providerStatus(instance(over), auth)).toEqual({ word, tone });
});

it("indexes the hub's sign-in statuses by provider", () => {
	const statuses = [
		{ provider: "codex-jesse-fsck.com", needsLogin: true },
		{ provider: "lunaroute" },
	] as AuthStatusResponse[];
	expect(authByProvider(statuses).get("codex-jesse-fsck.com")?.needsLogin).toBe(true);
	expect(authByProvider(statuses).has("meta")).toBe(false);
});

it("says how a provider signs in", () => {
	expect(signInKind({ authModes: ["oauth", "apiKey"] })).toBe("Account");
	expect(signInKind({ authModes: ["apiKey"] })).toBe("API key");
	expect(signInKind({ authModes: ["credentialJson"] })).toBe("API key");
	expect(signInKind({ authModes: [] })).toBe("None");
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/providers/providerStatus.test.ts`
Expected: FAIL: the module doesn't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/providers/providerStatus.ts
// A provider's sign-in state in words (spec 12's Providers; ruling 6), from
// its instance row and evener/auth/list's status for it. "Expires in 3d"
// waits for the sign-in expiry the hub doesn't expose yet (S11b).
import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";

export interface ProviderStatus {
	word: "Sign-in expired" | "Signed in" | "Key set" | "Not signed in" | "No key" | "No sign-in needed";
	/** Only an expired sign-in needs a person: it is also the Board's notice. */
	tone: "ink" | "attention";
}

export function providerStatus(
	instance: Pick<InstanceEntry, "activeSource" | "authModes" | "credentialRequired">,
	auth: Pick<AuthStatusResponse, "needsLogin"> | undefined,
): ProviderStatus {
	if (auth?.needsLogin) return { word: "Sign-in expired", tone: "attention" };
	if (instance.activeSource === "oauth") return { word: "Signed in", tone: "ink" };
	if (instance.activeSource === "none") {
		if (!instance.credentialRequired) return { word: "No sign-in needed", tone: "ink" };
		return { word: instance.authModes?.includes("oauth") ? "Not signed in" : "No key", tone: "ink" };
	}
	return { word: "Key set", tone: "ink" };
}

export function authByProvider(statuses: readonly AuthStatusResponse[]): ReadonlyMap<string, AuthStatusResponse> {
	return new Map(statuses.map((status) => [status.provider, status]));
}

/** How a provider signs in, for its detail page: an account (OAuth), a key
 * (an API key or a credential file), or nothing. */
export function signInKind(instance: Pick<InstanceEntry, "authModes">): "Account" | "API key" | "None" {
	const modes = instance.authModes ?? [];
	if (modes.includes("oauth")) return "Account";
	if (modes.includes("apiKey") || modes.includes("credentialJson")) return "API key";
	return "None";
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/providers/providerStatus.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit** (`feat(native): provider sign-in states in words`).

### Task 13: Providers in the Hub

**Files:**
- Create: `mobile-native/src/hub/ProvidersPage.tsx`, `mobile-native/src/hub/useAuthStatuses.ts`
- Modify:
  - `mobile-native/src/hub/HubSheet.tsx`: route `Providers: { hubId: string; focus?: string; signIn?: boolean }`.
  - `mobile-native/src/hub/HubHome.tsx`: Providers pushes.
  - `mobile-native/App.tsx` and `mobile-native/src/screens.tsx`: `Providers` goes.
  - The Board's notices: "Sign in".
- Delete: `mobile-native/src/ProvidersScreen.tsx`. Its tests, `ProvidersScreen.test.tsx` and `ProvidersScreen.recovery.test.tsx`, move to `ProvidersPage.test.tsx` and `ProvidersPage.recovery.test.tsx`.
- Test: the moved tests, rewritten to the new copy, plus `mobile-native/src/hub/useAuthStatuses.test.ts`.

**Interfaces:**
- Consumes:
  - Task 12;
  - `useCredentialStore()` (`src/credentialStore.ts`), `useProviderSurface(store)` (`src/providerSurface.ts`), `ProviderSignIn` (`src/providerSignIn.ts`), `ProviderEditor` and `appliedInstanceWrite`;
  - `activeSourceLabel`, `credentialLayers`, `fingerprintUnavailable`, `isEndpointConflict` and `staleListingHeld` from the package, exactly as `ProvidersScreen.tsx` uses them today.
- Produces: `useAuthStatuses(client: ConversationClientLike | null): ReadonlyMap<string, AuthStatusResponse>`. It reads `evener/auth/list` when the client is set and again on each `evener/auth/updated`, and keeps the last map through a failed read.

**Requirements (spec 12's Providers; rulings 6, 9 and 12):**
1. **Move, don't rewrite.** `ProvidersScreenBody`'s wiring moves unchanged into `ProvidersPage`, with its comments:
   - the credential store bound through `useCredentialStore`;
   - the sign-in flow's lifecycle, and the effect that hands it the connection;
   - the `writesRefused` gate.

   So does `Providers`' machinery:
   - `act`, `confirm`, `probeCredentials`, `editCredential` and `close`;
   - `editorVersion`;
   - the applied-write, endpoint-conflict and fingerprint messages.

   Only what renders changes. Every behavior today's tests pin stays pinned: move each test, then change only its copy and its queries.
2. **The list** (title "Providers"):
   - One group of instances: the name; the second line is the provider id, with " · default" on the default instance.
   - The value is the status: an amber `Tag` "Sign-in expired", or the word in ink-mid.
   - Each row has a chevron and opens the instance's detail sheet (ruling 9: a `Modal` page sheet over the list, as today).
   - No pull-to-refresh: the list follows the credential store's own notifications and `evener/auth/updated`.
   - `SheetStatus` sits on top, and `Connecting` shows before the first listing, in place of today's wall.
   - A "MANAGE" group at the bottom keeps "Add provider" (today's create flow), since provider management stays on the phone (ruling 12).
3. **The detail sheet** (titled with the instance's name, header "Done"):
   - The first group holds "Status" (the word, amber when expired), "Type" (the provider id) and "Sign-in" (`signInKind`).
   - "MODELS" lists the instance's models that aren't disabled, each id in Menlo, or "No models listed".
   - The actions group holds:
     - "Sign in", or "Sign in again" with a stored OAuth sign-in: OAuth instances, opening Task 14's sheet;
     - "Replace key", or "Set key" with none stored: `apiKey` instances;
     - "Replace credential JSON", or "Set credential JSON": `credentialJson` instances;
     - "Test connection": its result is one line under the group, "Works" or the test's own message.
   - Replacing a key pushes nothing. The group becomes a form: a secure field, "Paste the API key", with Save and Cancel, and the footer "The key is stored on the hub, not on this phone." The save path is today's `setApiKey` or `setCredentialJson`, fingerprint and all.
   - A "MANAGE" group keeps today's Edit, Make default, Clear stored key, Clear credentials and Remove, with their confirmations, as accent and danger rows (ruling 12).
   - Errors and warnings show as danger and attention footers.
4. **Deep links.** With `focus`, the page opens that instance's detail once on mount. With `signIn` too, it starts the sign-in flow as the detail's Sign in does. Then it clears both params.
5. **The Hub's home:** Providers pushes `Providers`, valued with the number of instances, plus an amber `Tag` "N to sign in" when any is "Sign-in expired".
6. **The Board's sign-in notice** becomes `navigation.navigate("Hub", { screen: "Providers", params: { hubId, focus: provider, signIn: true }, initial: false })` (ruling 25).

- [ ] **Step 1: Write the failing tests.**
  - `useAuthStatuses`: reads on a client, re-reads on `evener/auth/updated`, keeps the map through a failure, and reads nothing without a client.
  - The page: the moved cases, and:
    - the list's status words and the amber tag;
    - `focus` with `signIn` starts the flow;
    - the detail's rows per auth mode;
    - "Replace key" saves through the fingerprinted path;
    - "Test connection" shows its result;
    - no `/\bReconnect\b/`, and no pull-to-refresh (`refreshing` and `onRefresh` absent).
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/ProvidersPage.test.tsx src/hub/ProvidersPage.recovery.test.tsx src/hub/useAuthStatuses.test.ts`
- [ ] **Step 3: Implement**, then delete `ProvidersScreen.tsx` and its route.
- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hub src/board src/providerSignIn.test.ts src/credentialStore.test.ts && npm run check && make test-native-bundle`
- [ ] **Step 5: Commit** (`feat(native): Providers in the Hub`).

### Task 14: Signing in without leaving the app

**Files:**
- Modify:
  - `mobile-native/src/ProviderSignInSheet.tsx`;
  - `mobile-native/package.json`, `package-lock.json` and `Podfile.lock`: `expo-web-browser`, only if phase 3 didn't add it.
- Test: `mobile-native/src/ProviderSignInSheet.test.tsx`

**Interfaces:**
- Consumes:
  - `ProviderSignIn`'s snapshot (`phase`, `device`, `browser`, `busy`, `error`) and methods (`start`, `retryPoll`, `complete`, `setActive`), unchanged;
  - `setStringAsync` from `expo-clipboard`;
  - `openBrowserAsync` and `dismissBrowser` from `expo-web-browser`.

**Requirements (spec 12: "the code, a copy button, 'Open sign-in page', and automatic completion"):**
1. **Device flow**, titled "Sign in to <provider>":
   - The body: "The sign-in page opens inside the app, and this code is copied for you. Paste it when the page asks for it. The hub finishes signing in on its own."
   - The code in Menlo semibold 26, with "Copy code" beside it (today's copy logic and its failure copy).
   - The primary "Open sign-in page" copies the code first (`setStringAsync`), then opens `verificationUrl` in the in-app browser (`openBrowserAsync`). Keep today's URL check: http(s) only, no user info.
   - From then on the sheet says "Waiting for you to finish signing in…" and "Your code is <code>." (spec 12: the waiting state repeats the code).
2. **Authorized:** call `dismissBrowser()`, ignoring its rejection when no browser is open. Show "Signed in to <provider>" and "Sessions using it can continue.", and the header reads "Done". The flow already polls on its own (`ProviderSignIn`), which is the automatic completion.
3. **Errors, one action each:**
   - a poll error shows its message and "Check again" (`retryPoll`);
   - `expired` shows "The code expired." and "Start again";
   - `error` shows its message and "Start again".
   - "Check credential status" goes from every other phase: the flow checks on its own.
4. **The browser fallback** (`phase === "browser"`, a provider with no device flow) keeps today's steps. It opens the page in the in-app browser, then offers the redirect-URL field and "Finish sign-in", restyled.
5. **The connection:** `SheetStatus` replaces `ConnectionStatus` and its Reconnect. "Waiting for this hub to reconnect…" goes, because the status line says it. Controls that need the hub stay disabled while `!connected`, as today.

- [ ] **Step 1: Add expo-web-browser if it isn't a dependency** (`grep expo-web-browser mobile-native/package.json`). Otherwise `npx expo install expo-web-browser`, then regenerate the pod lock. Expected: the diff adds its pod only.
- [ ] **Step 2: Write the failing tests.**
  - Mocks: `expo-clipboard` (`setStringAsync` resolving `true`) and `expo-web-browser` (`openBrowserAsync`, and `dismissBrowser` rejecting).
  - Use a real `ProviderSignIn` over a credential store answering `deviceStart` from the test (see `src/providerSignIn.testkit.ts`).
  - Cases:
    - the device copy and the code;
    - "Open sign-in page" calls `setStringAsync(code)` before `openBrowserAsync(url)`;
    - the waiting copy repeats the code;
    - authorizing calls `dismissBrowser()` and shows "Signed in to codex-jesse-fsck.com";
    - a poll error offers "Check again", which calls `retryPoll`;
    - no text matches `/\bReconnect\b|Check credential status/`.
- [ ] **Step 3: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/ProviderSignInSheet.test.tsx`
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/ProviderSignInSheet.test.tsx src/providerSignIn.test.ts src/providerSignInRecovery.test.ts && npm run check && make test-native-bundle`
- [ ] **Step 6: Look in the simulator.** Sign in to a Codex instance: the page opens over the app, the code pastes, and the sheet turns "Signed in" and closes the page on its own.
- [ ] **Step 7: Commit and open PR 6** (`feat(native): Providers and sign-in in the Hub (phase 5, PR 6)`).

---

## PR 7: Plugins

### Task 15: Plugins in the Hub

**Files:**
- Create: `mobile-native/src/hub/PluginsPage.tsx`
- Modify:
  - `mobile-native/src/MarketplaceBrowser.tsx`: only what it renders, split into the Marketplaces and Browse segments.
  - `mobile-native/src/hub/HubSheet.tsx`: route `Plugins: { hubId: string; focus?: { plugin: string; marketplace: string } }`.
  - `mobile-native/src/hub/HubHome.tsx`, `mobile-native/App.tsx` and `mobile-native/src/screens.tsx`: `Plugins` goes from the root stack.
  - The Board's notices: "Plugins".
- Delete: `mobile-native/src/PluginsScreen.tsx`. Its tests, `PluginsScreen.test.tsx` and `PluginsScreen.reconnect.test.tsx`, move to `PluginsPage.test.tsx` and `PluginsPage.connection.test.tsx`.
- Test: the moved tests, and `MarketplaceBrowser.removal.test.tsx`, which keeps passing unchanged.

**Interfaces:**
- Consumes:
  - `createPluginsStore` and `createMarketplacesStore` (`@evener/appwire-client/state/extensions`);
  - `createPluginMutationGate`, `runGatedMutation` and `PLUGIN_MUTATION_BUSY` (`src/pluginMutationGate.ts`);
  - `MarketplaceBrowser`'s props, unchanged.

**Requirements (spec 12's Plugins; rulings 7 and 9):**
1. **Move, don't rewrite.** `PluginsScreenBody`'s state moves unchanged into `PluginsPage`, with its comments. That state is:
   - the mutation gate held above the connection's early returns;
   - the applied-removal guard with its per-name publication baselines;
   - `currentClient`, `markAppliedRemoval`, `reconcileAppliedRemovals`, `clearAddedMarketplace`, `clearMarketplaceWarning` and the marketplace warning.

   So does `Plugins`' store wiring (the stores, `connectionChanged`, `start` and `dispose`, `act`, `remove`). Only what renders changes. Every behavior today's tests pin stays pinned.
2. **Three segments** under the title "Plugins": `Segmented` Installed, Marketplaces, Browse.
   - **Installed:**
     - The footer: "“On by default” sets which plugins new sessions start with. You can still choose per session."
     - One group per marketplace, labelled with the marketplace's name as typed (`GroupLabel machine`).
     - Each row shows the plugin's name, and its version as the second line, with " · Upgrades automatically" when `autoUpgrade`, or "Broken" in danger ink. Its trailing switch is "On by default" (`enablePlugin`/`disablePlugin` through `act`, disabled while busy or not ready). Tapping the row opens its detail sheet.
     - The detail sheet (a `Modal` page sheet, header "Done") holds:
       - switches "On by default" and "Upgrade automatically";
       - "Upgrade" (accent), whose result line compares the entry's `version` and `gitCommitSha` before and after: "Already up to date", or "Upgraded to <version>" (ruling 7);
       - "Remove" (danger), with today's confirmation;
       - for a broken plugin, "This plugin is broken. Upgrade it or remove it." in danger ink.
   - **Marketplaces:**
     - Rows show a name, with the source as a Menlo second line (the repo or URL, or the path).
     - A row's detail offers "Refresh" and "Remove", with today's confirmation and applied-removal handling.
     - An accent "Add marketplace…" row opens today's add form: a GitHub repo or a URL.
   - **Browse:** the marketplaces' catalogs with "Install" per plugin, exactly as `MarketplaceBrowser` behaves today.
   - `MarketplaceBrowser` keeps its props, stores and fences; only its markup splits across the two segments.
3. **No pull-to-refresh.** Today's `refreshing`/`onRefresh` goes: the store follows `evener/plugin/updated` on its own. A failed list read shows its message as a footer with no button; the next notification or focus reads again.
4. **The connection:** `SheetStatus` and `Connecting` replace `ConnectionWall`, `ConnectionStatus` and `ModalConnectionStatus`.
5. **`focus`** opens that plugin's detail sheet once on mount, then clears the param.
6. **The Hub's home:** Plugins pushes `Plugins`, valued with the number installed. There is no update tag (ruling 7).
7. **The Board's plugin notice** becomes `navigation.navigate("Hub", { screen: "Plugins", params: { hubId, focus: { plugin, marketplace } }, initial: false })` (ruling 25).

- [ ] **Step 1: Move the tests and write the new cases.** The new cases:
  - the three segments;
  - the switch's enable and disable calls;
  - "Already up to date" and "Upgraded to 1.2.0" from the list the upgrade answers with;
  - `focus` opens the detail;
  - no `/\bReconnect\b/`, no `onRefresh`.

  `PluginsPage.connection.test.tsx` keeps what `PluginsScreen.reconnect.test.tsx` proves about a connection flap: a gate held across it, and a replaced client's late result ignored. It proves them without a Reconnect control.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/hub/PluginsPage.test.tsx src/hub/PluginsPage.connection.test.tsx`
- [ ] **Step 3: Implement**, then delete `PluginsScreen.tsx` and its route.
- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/hub src/board src/MarketplaceBrowser.removal.test.tsx src/marketplaceBrowserModel.test.ts src/pluginMutationGate.test.ts && npm run check && make test-native-bundle`
- [ ] **Step 5: Commit and open PR 7** (`feat(native): Plugins in the Hub (phase 5, PR 7)`).

---

## PR 8: the New session's rules and reads

PR 8 lands the pure rules, the phone's memory of starts, host-routed reads, and the creation store's new abilities. The New session sheet (PR 9) is their first consumer; say so in the PR description. PR 8 can run beside PR 1.

### Task 16: A setup and its rules

**Files:**
- Create: `mobile-native/src/newSession/launchSetup.ts`
- Test: `mobile-native/src/newSession/launchSetup.test.ts`

**Interfaces:**
- Consumes: `basename` and `LaunchConfigLayer` from `@evener/appwire-client`; `LOCAL_HOST` from `cmd/evener-hub/frontend/src/stores/hostRouting.ts`.
- Produces:
  - types: `OWNED_FIELDS`, `OwnedOverrides`, `ModelChoice`, `LaunchSetup`, `RememberedSetup`, `SessionSeed`, `HostMove`, `AccessLevel`, and `ACCESS_LEVELS`;
  - `ownedOverrides(layer)` and `withOwnedOverrides(layer, owned)`;
  - `newestSetup(history)`;
  - `moveToHost(cwd, hostLabel, projectExists, recentOnHost)` and `projectName(cwd)`;
  - `effortLabel(level)`, `accessOf(sandbox, hubDefault)` and `networkApplies(access)`;
  - `setupOf(form)`, `hostOfRef(ref)` and `modelFromId(id, models)`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/newSession/launchSetup.test.ts
import { describe, expect, it } from "vitest";
import {
	accessOf,
	effortLabel,
	hostOfRef,
	type LaunchSetup,
	modelFromId,
	moveToHost,
	networkApplies,
	newestSetup,
	ownedOverrides,
	projectName,
	setupOf,
	withOwnedOverrides,
} from "./launchSetup";

const setup = (over: Partial<LaunchSetup> = {}): LaunchSetup => ({
	host: "local",
	cwd: "/home/jesse/git/evener",
	model: { provider: "lunaroute", model: "glm-5.3-vision" },
	effort: "xhigh",
	overrides: { enabledPlugins: ["superpowers", "go"], sandbox: "workspace-write" },
	...over,
});

describe("the overrides the sheet owns", () => {
	it("picks only the owned fields, copying lists", () => {
		const plugins = ["superpowers"];
		const owned = ownedOverrides({
			enabledPlugins: plugins,
			sandbox: "read-only",
			maxRounds: 100,
			env: { A: "1" },
			verbose: true,
		});
		expect(owned).toEqual({ enabledPlugins: ["superpowers"], sandbox: "read-only", maxRounds: 100 });
		expect(owned.enabledPlugins).not.toBe(plugins);
	});

	it("replaces the owned fields and keeps everything else", () => {
		expect(withOwnedOverrides({ sandbox: "restricted", maxRounds: 5, env: { A: "1" } }, { sandbox: "read-only" })).toEqual({
			sandbox: "read-only",
			env: { A: "1" },
		});
	});
});

describe("the newest start", () => {
	const history = [
		{ setup: setup({ effort: "high" }), at: 1 },
		{ setup: setup({ cwd: "/home/jesse/git/docs", effort: "max" }), at: 3 },
		{ setup: setup({ effort: "xhigh" }), at: 2 },
	];

	it("is nothing when nothing was ever started from this phone", () => {
		expect(newestSetup([])).toBeNull();
	});

	it("opens a new sheet on the newest start", () => {
		expect(newestSetup(history)?.cwd).toBe("/home/jesse/git/docs");
	});
});

describe("changing host (ruling 17)", () => {
	it("keeps a project the new host has", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", true, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/home/jesse/git/evener",
			note: null,
		});
	});

	it("moves to the host's most recent project and says so", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", false, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/Users/jesse/git/docs",
			note: "evener isn't on paradise-park, so the project changed to docs.",
		});
	});

	it("asks for a project when the host remembers none", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", false, [])).toEqual({
			cwd: "",
			note: "evener isn't on paradise-park. Choose a project.",
		});
	});

	it("fills an empty project from the host's most recent one, quietly", () => {
		expect(moveToHost("", "paradise-park", false, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/Users/jesse/git/docs",
			note: null,
		});
	});

	it("names a project by its folder", () => {
		expect(projectName("/home/jesse/git/evener/")).toBe("evener");
	});
});

describe("labels", () => {
	it("spells efforts as spec 11's Effort control does", () => {
		expect(["low", "medium", "high", "xhigh", "max", "minimal", "none", "turbo"].map(effortLabel)).toEqual([
			"Low",
			"Med",
			"High",
			"XHigh",
			"Max",
			"Minimal",
			"Off",
			"turbo",
		]);
	});

	it("names the access a setup runs with", () => {
		expect(accessOf("read-only", "off").label).toBe("Read-only");
		expect(accessOf(undefined, "workspace-write").label).toBe("Workspace write");
		expect(accessOf(undefined, undefined).label).toBe("Full access");
		expect(accessOf("sealed", undefined)).toEqual({ mode: "sealed", label: "sealed", detail: "" });
	});

	it("offers the network switch only inside a sandbox", () => {
		expect(networkApplies(accessOf("off", undefined))).toBe(false);
		expect(networkApplies(accessOf("restricted", undefined))).toBe(true);
	});
});

describe("from the form and from a session", () => {
	it("reads the creation form as a setup", () => {
		expect(
			setupOf({
				source: "paradise-park",
				cwd: " /Users/jesse/git/evener ",
				model: { provider: "lunaroute", model: "glm-5.3-vision" },
				reasoning: "high",
				launchOverrides: { sandbox: "restricted", env: { A: "1" } },
			}),
		).toEqual({
			host: "paradise-park",
			cwd: "/Users/jesse/git/evener",
			model: { provider: "lunaroute", model: "glm-5.3-vision" },
			effort: "high",
			overrides: { sandbox: "restricted" },
		});
	});

	it("finds a session's host in its ref", () => {
		expect(hostOfRef("paradise-park:0123456789abcdefghijkl")).toBe("paradise-park");
		expect(hostOfRef("local:0123456789abcdefghijkl")).toBe("local");
		expect(hostOfRef("unqualified")).toBe("local");
	});

	it("finds a model named the way a session names it", () => {
		const models = [
			{ provider: "lunaroute", model: "glm-5.3" },
			{ provider: "zai", model: "glm-5.3" },
			{ provider: "meta", model: "muse-spark-1.3" },
		];
		expect(modelFromId("lunaroute/glm-5.3", models)).toBe(models[0]);
		expect(modelFromId("muse-spark-1.3", models)).toBe(models[2]);
		expect(modelFromId("glm-5.3", models)).toBeNull();
		expect(modelFromId("gpt-5.6", models)).toBeNull();
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/newSession/launchSetup.test.ts`
Expected: FAIL: `Cannot find module './launchSetup'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/newSession/launchSetup.ts
// A new session's setup (spec 11): where it runs and how. The sheet's rows
// and a host change speak in this shape, and every rule applied to it is a
// pure function here.
import { basename, type LaunchConfigLayer } from "@evener/appwire-client";
import { LOCAL_HOST } from "../../../cmd/evener-hub/frontend/src/stores/hostRouting";

/** The launch overrides the sheet owns: Plugins, Access and More options.
 * Everything else stays at the hub's defaults on the phone (spec 11). */
export const OWNED_FIELDS = [
	"enabledPlugins",
	"sandbox",
	"sandboxNet",
	"contextStrategy",
	"maxSubagentDepth",
	"maxRounds",
] as const satisfies readonly (keyof LaunchConfigLayer)[];

export type OwnedOverrides = Pick<LaunchConfigLayer, (typeof OWNED_FIELDS)[number]>;

export interface ModelChoice {
	provider: string;
	model: string;
}

export interface LaunchSetup {
	/** "local" is the hub's own machine; anything else is a host's name. */
	host: string;
	cwd: string;
	/** null is the hub's default model. */
	model: ModelChoice | null;
	/** "" is the model's own default effort. */
	effort: string;
	overrides: OwnedOverrides;
}

/** A remembered start: its setup, and when a session started with it (ms). */
export interface RememberedSetup {
	setup: LaunchSetup;
	at: number;
}

/** What "New session like this" copies from a session (ruling 24). */
export interface SessionSeed {
	host: string;
	cwd: string;
	/** The session's model as it reports it: "provider/model", or a bare model. */
	model?: string;
	effort?: string;
}

export function ownedOverrides(layer: LaunchConfigLayer): OwnedOverrides {
	const owned: OwnedOverrides = {};
	if (layer.enabledPlugins !== undefined) owned.enabledPlugins = [...layer.enabledPlugins];
	if (layer.sandbox !== undefined) owned.sandbox = layer.sandbox;
	if (layer.sandboxNet !== undefined) owned.sandboxNet = layer.sandboxNet;
	if (layer.contextStrategy !== undefined) owned.contextStrategy = layer.contextStrategy;
	if (layer.maxSubagentDepth !== undefined) owned.maxSubagentDepth = layer.maxSubagentDepth;
	if (layer.maxRounds !== undefined) owned.maxRounds = layer.maxRounds;
	return owned;
}

/** `layer` with its owned fields replaced by `owned`'s. A field `owned` leaves
 * out is removed, so a setup with no sandbox falls back to the hub's. */
export function withOwnedOverrides(layer: LaunchConfigLayer, owned: OwnedOverrides): LaunchConfigLayer {
	const next: LaunchConfigLayer = { ...layer };
	for (const field of OWNED_FIELDS) delete next[field];
	return { ...next, ...ownedOverrides(owned) };
}

/** The newest setup started from this phone: where a sheet with nothing in
 * it yet begins. */
export function newestSetup(history: readonly RememberedSetup[]): LaunchSetup | null {
	let newest: RememberedSetup | null = null;
	for (const entry of history) if (!newest || entry.at > newest.at) newest = entry;
	return newest?.setup ?? null;
}

export interface HostMove {
	cwd: string;
	/** The line under the Host row saying what moved, or null. */
	note: string | null;
}

/** Where the project goes when the host changes (spec 11; ruling 17): it stays
 * when the new host has it, else moves to that host's most recent project and
 * says so. With no project chosen yet, it quietly takes that recent project. */
export function moveToHost(
	cwd: string,
	hostLabel: string,
	projectExists: boolean,
	recentOnHost: readonly string[],
): HostMove {
	const recent = recentOnHost[0];
	if (!cwd) return { cwd: recent ?? "", note: null };
	if (projectExists) return { cwd, note: null };
	const name = projectName(cwd);
	if (!recent) return { cwd: "", note: `${name} isn't on ${hostLabel}. Choose a project.` };
	return { cwd: recent, note: `${name} isn't on ${hostLabel}, so the project changed to ${projectName(recent)}.` };
}

/** A project's name: its folder (spec 11's "Project evener"). */
export function projectName(cwd: string): string {
	return basename(cwd) || cwd;
}

const EFFORT_LABELS: Record<string, string> = {
	none: "Off",
	minimal: "Minimal",
	low: "Low",
	medium: "Med",
	high: "High",
	xhigh: "XHigh",
	max: "Max",
};

/** An effort level's label on New session's Effort control, spelled as spec
 * 11's frame spells them: Low, Med, High, XHigh, Max. Phase 3's effortName
 * (src/session/sessionFacts.ts) spells them in full for the composer's chip;
 * a segment needs the short form. A level this build doesn't know shows as
 * sent. */
export function effortLabel(level: string): string {
	return EFFORT_LABELS[level] ?? level;
}

export interface AccessLevel {
	/** The launch override's `sandbox` value. */
	mode: string;
	label: string;
	detail: string;
}

const FULL_ACCESS: AccessLevel = { mode: "off", label: "Full access", detail: "No sandbox: reads and writes anywhere" };

/** Spec 11's four access levels, in the hub's sandbox terms (schema.go's
 * sandbox option). */
export const ACCESS_LEVELS: readonly AccessLevel[] = [
	FULL_ACCESS,
	{ mode: "workspace-write", label: "Workspace write", detail: "Writes only in the project; reads anywhere but secrets" },
	{ mode: "read-only", label: "Read-only", detail: "Writes nothing; reads anywhere but secrets" },
	{ mode: "restricted", label: "Restricted", detail: "Reads and writes only in the project" },
];

/** The access a setup runs with: its own sandbox, else the hub's default for
 * the project (launch/resolve's effective value), else the schema's "off". A
 * mode this build doesn't know shows as the hub named it. */
export function accessOf(sandbox: string | undefined, hubDefault: string | undefined): AccessLevel {
	const mode = sandbox || hubDefault || FULL_ACCESS.mode;
	return ACCESS_LEVELS.find((level) => level.mode === mode) ?? { mode, label: mode, detail: "" };
}

/** Network matters only inside a sandbox (the schema's sandbox_net: "Has no
 * effect unless a sandbox mode is set"), so its switch shows only then. */
export function networkApplies(access: AccessLevel): boolean {
	return access.mode !== FULL_ACCESS.mode;
}

/** The creation form's fields as a setup. */
export function setupOf(form: {
	source: string;
	cwd: string;
	model: ModelChoice | null;
	reasoning: string;
	launchOverrides: LaunchConfigLayer;
}): LaunchSetup {
	return {
		host: form.source,
		cwd: form.cwd.trim(),
		model: form.model ? { provider: form.model.provider, model: form.model.model } : null,
		effort: form.reasoning,
		overrides: ownedOverrides(form.launchOverrides),
	};
}

/** The host a session runs on, from its ref ("<host>:<session>", appwire/refs.go). */
export function hostOfRef(ref: string): string {
	const colon = ref.indexOf(":");
	return colon > 0 ? ref.slice(0, colon) : LOCAL_HOST;
}

/** A model named the way a session or a launch override names one:
 * "provider/model", or a bare model only one provider offers. */
export function modelFromId<T extends ModelChoice>(id: string, models: readonly T[]): T | null {
	const matches = models.filter((model) => `${model.provider}/${model.model}` === id || model.model === id);
	return matches.length === 1 ? (matches[0] ?? null) : null;
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/newSession/launchSetup.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit** (`feat(native): a new session's setup and its rules`).

### Task 17: The phone's memory of starts

**Files:**
- Create: `mobile-native/src/newSession/launchMemory.ts`, `mobile-native/src/newSession/nativeLaunchMemory.ts`
- Modify: `mobile-native/src/ConnectionProvider.tsx` (`removeHub` forgets it)
- Test: `mobile-native/src/newSession/launchMemory.test.ts`

**Interfaces:**
- Consumes: Task 16.
- Produces:
  - `interface LaunchMemoryStorage { getItemSync; setItemSync; removeItemSync }`;
  - `historyKey(hubId)` and `HISTORY_LIMIT = 50`;
  - `class LaunchMemory`: constructor `(storage, hubId)`; `history()` and `recordStart(setup, at)`;
  - `forgetLaunchMemory(storage, hubId)`;
  - `launchMemory(hubId): LaunchMemory` (one per hub, on `expo-sqlite/kv-store`) and `forgetLaunchMemoryForHub(hubId)`.

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/newSession/launchMemory.test.ts
import { describe, expect, it } from "vitest";
import { forgetLaunchMemory, HISTORY_LIMIT, historyKey, LaunchMemory } from "./launchMemory";
import type { LaunchSetup } from "./launchSetup";

function memory(values = new Map<string, string>()) {
	return {
		values,
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItemSync: (key: string) => {
			values.delete(key);
		},
	};
}
const setup = (over: Partial<LaunchSetup> = {}): LaunchSetup => ({
	host: "local",
	cwd: "/home/jesse/git/evener",
	model: { provider: "lunaroute", model: "deepseek-4.1-flash" },
	effort: "xhigh",
	overrides: { sandbox: "workspace-write" },
	...over,
});

describe("starts", () => {
	it("keeps the newest start per host and project, newest first", () => {
		const starts = new LaunchMemory(memory(), "hub-a");
		starts.recordStart(setup({ effort: "high" }), 1);
		starts.recordStart(setup({ cwd: "/home/jesse/git/docs" }), 2);
		starts.recordStart(setup({ effort: "max" }), 3);
		expect(starts.history().map((entry) => [entry.setup.cwd, entry.setup.effort, entry.at])).toEqual([
			["/home/jesse/git/evener", "max", 3],
			["/home/jesse/git/docs", "xhigh", 2],
		]);
	});

	it("forgets the oldest past the limit", () => {
		const starts = new LaunchMemory(memory(), "hub-a");
		for (let index = 0; index <= HISTORY_LIMIT; index++) starts.recordStart(setup({ cwd: `/p/${index}` }), index);
		expect(starts.history()).toHaveLength(HISTORY_LIMIT);
		expect(starts.history().some((entry) => entry.setup.cwd === "/p/0")).toBe(false);
	});

	it("leaves itself unchanged when the phone can't store the change", () => {
		const storage = memory();
		const starts = new LaunchMemory(storage, "hub-a");
		starts.recordStart(setup(), 1);
		storage.setItemSync = () => {
			throw new Error("disk full");
		};
		expect(() => starts.recordStart(setup({ cwd: "/home/jesse/git/docs" }), 2)).toThrow("disk full");
		expect(starts.history().map((entry) => entry.setup.cwd)).toEqual(["/home/jesse/git/evener"]);
	});
});

describe("stored values", () => {
	it("drops entries this build can't read and keeps the rest", () => {
		const storage = memory(
			new Map([
				[
					historyKey("hub-a"),
					JSON.stringify([
						{ setup: { host: 7 }, at: 1 },
						{ setup: setup(), at: 2 },
					]),
				],
				[historyKey("hub-b"), "{not json"],
			]),
		);
		expect(new LaunchMemory(storage, "hub-a").history().map((entry) => entry.at)).toEqual([2]);
		expect(new LaunchMemory(storage, "hub-b").history()).toEqual([]);
	});

	it("forgets one hub and keeps another's (Review Focus 5)", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a").recordStart(setup(), 1);
		new LaunchMemory(storage, "hub-b").recordStart(setup(), 2);
		forgetLaunchMemory(storage, "hub-a");
		expect(new LaunchMemory(storage, "hub-a").history()).toEqual([]);
		expect(new LaunchMemory(storage, "hub-b").history().map((entry) => entry.at)).toEqual([2]);
	});
});
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd mobile-native && npx vitest run src/newSession/launchMemory.test.ts`
Expected: FAIL: the module doesn't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/newSession/launchMemory.ts
// What this phone remembers about starting sessions on one hub: the setups
// sessions were started with, so New session opens on the newest one (spec
// 11). It is per hub, because a setup names that hub's hosts and folders.
import type { LaunchConfigLayer } from "@evener/appwire-client";
import { type LaunchSetup, ownedOverrides, type RememberedSetup } from "./launchSetup";

export interface LaunchMemoryStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

export const historyKey = (hubId: string) => `evener.native.launch-history.${hubId}`;

/** Starts remembered per hub: one per host and project, enough for every
 * project in use (the spec's fleet has 14) and small enough to rewrite on
 * every start. */
export const HISTORY_LIMIT = 50;

function record(value: unknown): Record<string, unknown> | null {
	return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

/** A stored setup this build can read, or null. Unknown override fields are
 * dropped: only what the sheet owns comes back. */
function toSetup(value: unknown): LaunchSetup | null {
	const setup = record(value);
	const overrides = record(setup?.overrides);
	const model = setup?.model === null ? null : record(setup?.model);
	if (
		!setup ||
		!overrides ||
		typeof setup.host !== "string" ||
		typeof setup.cwd !== "string" ||
		typeof setup.effort !== "string" ||
		(setup.model !== null && (!model || typeof model.provider !== "string" || typeof model.model !== "string"))
	)
		return null;
	const plugins = overrides.enabledPlugins;
	if (plugins !== undefined && !(Array.isArray(plugins) && plugins.every((name) => typeof name === "string"))) return null;
	for (const [field, kind] of [
		["sandbox", "string"],
		["sandboxNet", "boolean"],
		["contextStrategy", "string"],
		["maxSubagentDepth", "number"],
		["maxRounds", "number"],
	] as const)
		if (overrides[field] !== undefined && typeof overrides[field] !== kind) return null;
	return {
		host: setup.host,
		cwd: setup.cwd,
		model: model ? { provider: model.provider as string, model: model.model as string } : null,
		effort: setup.effort,
		// Every owned field was type-checked above; ownedOverrides keeps only them.
		overrides: ownedOverrides(overrides as LaunchConfigLayer),
	};
}

function toRemembered(value: unknown): RememberedSetup | null {
	const entry = record(value);
	const setup = toSetup(entry?.setup);
	return entry && setup && typeof entry.at === "number" ? { setup, at: entry.at } : null;
}

function parseList<T>(raw: string | null, item: (value: unknown) => T | null): T[] {
	if (!raw) return [];
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return [];
	}
	if (!Array.isArray(value)) return [];
	return value.flatMap((entry) => {
		const parsed = item(entry);
		return parsed ? [parsed] : [];
	});
}

export class LaunchMemory {
	private historyList: RememberedSetup[];

	constructor(
		private readonly storage: LaunchMemoryStorage,
		private readonly hubId: string,
	) {
		this.historyList = parseList(storage.getItemSync(historyKey(hubId)), toRemembered);
	}

	history(): readonly RememberedSetup[] {
		return this.historyList;
	}

	/** Remembers a start: it replaces older starts for the same host and
	 * project, and the oldest fall off past the limit. */
	recordStart(setup: LaunchSetup, at: number): void {
		const others = this.historyList.filter(
			(entry) => entry.setup.host !== setup.host || entry.setup.cwd !== setup.cwd,
		);
		this.writeHistory([{ setup, at }, ...others].slice(0, HISTORY_LIMIT));
	}

	/** Stores first, so a failed write leaves this memory as it was. */
	private writeHistory(next: RememberedSetup[]): void {
		this.storage.setItemSync(historyKey(this.hubId), JSON.stringify(next));
		this.historyList = next;
	}
}

export function forgetLaunchMemory(storage: LaunchMemoryStorage, hubId: string): void {
	storage.removeItemSync(historyKey(hubId));
}
```

```ts
// mobile-native/src/newSession/nativeLaunchMemory.ts
import { Storage } from "expo-sqlite/kv-store";
import { forgetLaunchMemory, LaunchMemory } from "./launchMemory";

const memories = new Map<string, LaunchMemory>();

/** The one LaunchMemory per hub, shared by every New session sheet. */
export function launchMemory(hubId: string): LaunchMemory {
	let memory = memories.get(hubId);
	if (!memory) {
		memory = new LaunchMemory(Storage, hubId);
		memories.set(hubId, memory);
	}
	return memory;
}

export function forgetLaunchMemoryForHub(hubId: string): void {
	forgetLaunchMemory(Storage, hubId);
	memories.delete(hubId);
}
```

In `ConnectionProvider.removeHub`'s `removeHub(hubId)` callback, call `forgetLaunchMemoryForHub(hubId);` beside `drafts.removeHub(hubId)`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/newSession/launchMemory.test.ts src/removeHub.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit** (`feat(native): starts remembered per hub on the phone`).

### Task 18: Reads that go to the chosen host

**Files:**
- Modify:
  - `cmd/evener-hub/frontend/src/stores/hostRouting.ts`: `hostRequest` takes `Pick<AppwireClientLike, "request">`;
  - `mobile/src/services/newSession.ts`;
  - `mobile-native/src/hubPaths.ts`: an optional host.
- Test:
  - `cmd/evener-hub/frontend/src/stores/hostRouting.test.ts`;
  - `mobile/src/services/newSession.test.ts`;
  - `mobile-native/src/hubPaths.test.ts`.

**Interfaces:**
- Consumes: `hostRequest` and `isLocalHost` from `cmd/evener-hub/frontend/src/stores/hostRouting.ts` (ruling 2).
- Produces:
  - `NewSessionParams` gains `source?: string`;
  - `recentProjects(host?)`, `models(params?, host?)` and `previewPlugins(params, host?)` gain a trailing host, so today's callers don't change;
  - new methods: `directoryExists(host, path): Promise<boolean>`, `createDirectory(host, path): Promise<string>`, `branch(host, cwd): Promise<string | null>` and `resolveLaunch(host, cwd, launchOverrides): Promise<LaunchConfigResolved>`;
  - `new HubPaths(client, includeFiles?, host?)`.

- [ ] **Step 1: Write the failing tests.** In `hostRouting.test.ts`, add:

```ts
test("forwards through a client that offers only request", async () => {
	const calls: unknown[][] = [];
	const client = {
		request: async (...args: unknown[]) => {
			calls.push(args);
			return { data: [] };
		},
	} as unknown as Parameters<typeof hostRequest>[0];
	await hostRequest(client, "paradise-park", "model/list", {});
	expect(calls).toEqual([["evener/host/request", { host: "paradise-park", method: "model/list", params: {} }, undefined]]);
});
```

In `mobile/src/services/newSession.test.ts`, add inside `describe("NewSessionService", …)`:

```ts
  it("asks the hub's own machine directly", async () => {
    const client = new FakeAppwireClient();
    client.on("evener/projects/recent", () => ({ data: ["/home/jesse/git/evener"] }) as ProjectsRecentResponse);
    const service = createNewSessionService(client);
    expect(await service.recentProjects()).toEqual(["/home/jesse/git/evener"]);
    expect(await service.recentProjects("local")).toEqual(["/home/jesse/git/evener"]);
    expect(client.calls.map((call) => call.method)).toEqual(["evener/projects/recent", "evener/projects/recent"]);
  });

  it("asks another host through the hub, method by method", async () => {
    const client = new FakeAppwireClient();
    client.on("evener/host/request", (request) => {
      switch (request.method) {
        case "evener/projects/recent":
          return { data: ["/Users/jesse/git/evener"] };
        case "model/list":
          return { data: [{ provider: "lunaroute", model: "glm-5.3-vision" }] };
        case "evener/plugin/preview":
          return { plugins: [] };
        case "evener/path/validate":
          return { path: "/Users/jesse/git/evener", valid: true };
        case "evener/dirs/create":
          return { path: "/Users/jesse/git/scratch", created: true };
        case "evener/git/head":
          return { head: "main" };
        case "evener/launch/resolve":
          return { effective: { sandbox: "workspace-write" }, layers: {}, provenance: {} };
        default:
          throw new Error(`unexpected ${request.method}`);
      }
    });
    const service = createNewSessionService(client);
    const cwd = "/Users/jesse/git/evener";
    expect(await service.recentProjects("paradise-park")).toEqual([cwd]);
    await service.models({ cwd }, "paradise-park");
    await service.previewPlugins({ cwd }, "paradise-park");
    expect(await service.directoryExists("paradise-park", cwd)).toBe(true);
    expect(await service.createDirectory("paradise-park", "/Users/jesse/git/scratch")).toBe("/Users/jesse/git/scratch");
    expect(await service.branch("paradise-park", cwd)).toBe("main");
    expect((await service.resolveLaunch("paradise-park", cwd, {})).effective.sandbox).toBe("workspace-write");
    expect(client.calls.map((call) => call.params)).toEqual([
      { host: "paradise-park", method: "evener/projects/recent", params: {} },
      { host: "paradise-park", method: "model/list", params: { cwd } },
      { host: "paradise-park", method: "evener/plugin/preview", params: { cwd } },
      { host: "paradise-park", method: "evener/path/validate", params: { path: cwd, kind: "dir" } },
      { host: "paradise-park", method: "evener/dirs/create", params: { path: "/Users/jesse/git/scratch" } },
      { host: "paradise-park", method: "evener/git/head", params: { cwd } },
      { host: "paradise-park", method: "evener/launch/resolve", params: { cwd } },
    ]);
  });

  it("says a folder that isn't a repository has no branch", async () => {
    const client = new FakeAppwireClient();
    client.on("evener/git/head", () => ({ head: "" }));
    expect(await createNewSessionService(client).branch("local", "/tmp")).toBeNull();
  });

  it("starts on another host with its source, and on the hub's own machine without one", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/start", () => ({ thread: makeThread(), turn: makeTurn() }) as ThreadStartResponse);
    const service = createNewSessionService(client);
    await service.start({ cwd: "/Users/jesse/git/evener", source: "paradise-park" });
    await service.start({ cwd: "/home/jesse/git/evener", source: "local" });
    await service.start({ cwd: "/home/jesse/git/evener" });
    expect(client.calls.map((call) => call.params)).toEqual([
      { cwd: "/Users/jesse/git/evener", source: "paradise-park" },
      { cwd: "/home/jesse/git/evener" },
      { cwd: "/home/jesse/git/evener" },
    ]);
  });
```

In `hubPaths.test.ts`, add a case: `new HubPaths(client, false, "paradise-park").load("/Users/jesse/")` sends `evener/host/request` with `{ host: "paradise-park", method: "evener/paths/complete", params: { prefix: "/Users/jesse/", includeFiles: false, limit: 100 } }`. Without a host, it still sends `evener/paths/complete` directly. Follow the file's existing fake.

- [ ] **Step 2: Run the tests and watch them fail.** Run:
  - `cd mobile-native && npm run test:shared -- newSession && npx vitest run src/hubPaths.test.ts`
  - `cd cmd/evener-hub/frontend && npx vitest run src/stores/hostRouting.test.ts`

  Expected: the new cases fail. The routing case fails at `npm run check` first: the one-method client doesn't satisfy today's type.
- [ ] **Step 3: Implement.**

  In `hostRouting.ts`, change `hostRequest`'s first parameter to `client: Pick<AppwireClientLike, "request">`. Nothing else in the function changes, and every caller already passes a full client.

  In `mobile/src/services/newSession.ts`, import `hostRequest` and `isLocalHost` from `../../../cmd/evener-hub/frontend/src/stores/hostRouting`, plus the new types. Then:

```ts
export interface NewSessionParams {
  /** The host to start on; absent or "local" is the hub's own machine. */
  source?: string;
  cwd: string;
  input?: InputItem[];
  modelProvider?: string;
  model?: string;
  harness?: string;
  reasoningEffort?: string;
  launchOverrides?: LaunchConfigLayer;
}

export interface NewSessionService {
  start(params: NewSessionParams): Promise<{ thread: Thread; turn: Turn }>;
  recentProjects(host?: string): Promise<string[]>;
  harnesses(): Promise<HarnessDescriptor[]>;
  models(params?: ModelListParams, host?: string): Promise<ModelListResponse>;
  previewPlugins(params: PluginPreviewParams, host?: string): Promise<PluginPreviewResponse>;
  directoryExists(host: string, path: string): Promise<boolean>;
  createDirectory(host: string, path: string): Promise<string>;
  branch(host: string, cwd: string): Promise<string | null>;
  resolveLaunch(host: string, cwd: string, launchOverrides: LaunchConfigLayer): Promise<LaunchConfigResolved>;
}
```

  In `start`, after the `cwd` line, add `if (params.source !== undefined && !isLocalHost(params.source)) wireParams.source = params.source;`. The other methods become:

```ts
    async recentProjects(host) {
      const response = await hostRequest(client, host, "evener/projects/recent", {});
      return response.data ?? [];
    },
    async models(params = {}, host) {
      return hostRequest(client, host, "model/list", params);
    },
    async previewPlugins(params, host) {
      return hostRequest(client, host, "evener/plugin/preview", params);
    },
    async directoryExists(host, path) {
      const response = await hostRequest(client, host, "evener/path/validate", { path, kind: "dir" });
      return response.valid;
    },
    async createDirectory(host, path) {
      const response = await hostRequest(client, host, "evener/dirs/create", { path });
      return response.path;
    },
    async branch(host, cwd) {
      const response = await hostRequest(client, host, "evener/git/head", { cwd });
      return response.head || null;
    },
    async resolveLaunch(host, cwd, launchOverrides) {
      return hostRequest(client, host, "evener/launch/resolve", {
        cwd,
        ...(Object.keys(launchOverrides).length > 0 ? { launchOverrides } : {}),
      });
    },
```

  In `hubPaths.ts`, add a third constructor parameter, `private host?: string`. `load` calls `hostRequest(this.client, this.host, "evener/paths/complete", { prefix, includeFiles: this.includeFiles, limit: 100 })` in place of `this.client.request(...)`.
- [ ] **Step 4: Run the tests and watch them pass**, with the same commands plus `npm run check` in `mobile-native` and `cd cmd/evener-hub/frontend && npx biome check --write src/stores/hostRouting.ts src/stores/hostRouting.test.ts`.
- [ ] **Step 5: Commit** (`feat(native): launch reads go to the chosen host`).

### Task 19: The creation store learns hosts and setups

**Files:**
- Modify: `mobile-native/src/newSession.ts`, `mobile-native/src/creationDraftRepository.ts`
- Test: `mobile-native/src/newSession.test.ts`, `mobile-native/src/creationDraftRepository.test.ts`

**Interfaces:**
- Consumes: Tasks 16 and 18.
- Produces, on the store's state:
  - `source: string` and `hostNote: string | null`;
  - `changeHost(host: string, hostLabel: string): Promise<void>`;
  - `applySetup(setup: LaunchSetup): void` and `applySeed(seed: SessionSeed): void`.

  Also: `CreationDraft.source?: string` (ruling 28); `creationModel` now matches through `modelFromId`.

- [ ] **Step 1: Write the failing tests.** Add to `newSession.test.ts`, reusing its `setup()` and `deferred()`:

```ts
const answer = (calls: ReturnType<typeof setup>["calls"], method: string, forwarded: string | null, value: unknown) => {
  const call = calls.find(
    (c) =>
      c.method === method &&
      (forwarded === null || (c.params as { method?: string }).method === forwarded),
  );
  if (!call) throw new Error(`no ${forwarded ?? method} request`);
  calls.splice(calls.indexOf(call), 1);
  call.response.resolve(value);
};
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

// changeHost ends by reading the new host's models, so each test answers that
// read before awaiting the change.
it("reads another host's projects and models through the hub, and starts there (Review Focus 1)", async () => {
  const { store, calls } = setup();
  const moving = store.getState().changeHost("paradise-park", "paradise-park");
  answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/evener"] });
  await flush();
  expect(store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener", hostNote: null });
  answer(calls, "evener/host/request", "model/list", { data: [model] });
  await moving;
  const started = store.getState().submit();
  const start = calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ cwd: "/Users/jesse/git/evener", source: "paradise-park" });
  answer(calls, "thread/start", null, { thread: { id: "t", evener: { ref: "paradise-park:t" } }, turn: {} });
  expect(await started).toMatchObject({ status: "created" });
});

it("keeps the project when the new host has it, and moves it with a note when it doesn't (ruling 17)", async () => {
  const { store, calls } = setup();
  await store.getState().setCwd("/home/jesse/git/evener", false);
  const kept = store.getState().changeHost("paradise-park", "paradise-park");
  answer(calls, "evener/host/request", "evener/path/validate", { path: "/home/jesse/git/evener", valid: true });
  answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/docs"] });
  await flush();
  expect(store.getState()).toMatchObject({ cwd: "/home/jesse/git/evener", hostNote: null });
  answer(calls, "evener/host/request", "model/list", { data: [model] });
  await kept;
  const moved = store.getState().changeHost("local", "magic-kingdom");
  answer(calls, "evener/path/validate", null, { path: "/home/jesse/git/evener", valid: false });
  answer(calls, "evener/projects/recent", null, { data: ["/home/jesse/git/docs"] });
  await flush();
  expect(store.getState()).toMatchObject({
    cwd: "/home/jesse/git/docs",
    hostNote: "evener isn't on magic-kingdom, so the project changed to docs.",
  });
  answer(calls, "model/list", null, { data: [model] });
  await moved;
});

it("keeps the project when the new host can't say whether it has it", async () => {
  const { store, calls } = setup();
  await store.getState().setCwd("/home/jesse/git/evener", false);
  const moving = store.getState().changeHost("paradise-park", "paradise-park");
  const validate = calls.find((c) => (c.params as { method?: string }).method === "evener/path/validate");
  validate?.response.reject(new Error("host went away"));
  answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/docs"] });
  await flush();
  expect(store.getState()).toMatchObject({ cwd: "/home/jesse/git/evener", hostNote: null });
  answer(calls, "evener/host/request", "model/list", { data: [model] });
  await moving;
});

it("drops answers for the host it just left (Review Focus 2)", async () => {
  const { store, calls } = setup();
  const leaving = store.getState().changeHost("paradise-park", "paradise-park");
  const staleRecent = calls.find((c) => (c.params as { method?: string }).method === "evener/projects/recent");
  const arriving = store.getState().changeHost("local", "magic-kingdom");
  answer(calls, "evener/projects/recent", null, { data: ["/home/jesse/git/evener"] });
  await flush();
  answer(calls, "model/list", null, { data: [model] });
  await arriving;
  staleRecent?.response.resolve({ data: ["/Users/jesse/git/elsewhere"] });
  await leaving;
  expect(store.getState()).toMatchObject({ source: "local", cwd: "/home/jesse/git/evener" });
  expect(store.getState().projects).toEqual(["/home/jesse/git/evener"]);
  expect(calls.some((c) => (c.params as { method?: string }).method === "model/list")).toBe(false);
});

it("applies a setup and settles its model against the host's list", async () => {
  const { store, calls } = setup();
  store.getState().applySetup({
    host: "local",
    cwd: "/project",
    model: { provider: "p", model: "a" },
    effort: "high",
    overrides: { sandbox: "read-only", enabledPlugins: ["superpowers"] },
  });
  expect(store.getState()).toMatchObject({
    source: "local",
    cwd: "/project",
    reasoning: "high",
    launchOverrides: { sandbox: "read-only", enabledPlugins: ["superpowers"] },
  });
  answer(calls, "model/list", null, { data: [model] });
  await flush();
  expect(store.getState().model).toEqual(model);
  expect(store.getState().reasoning).toBe("high");
});

it("applies a session's seed and finds its model once the host's models arrive", async () => {
  const { store, calls } = setup();
  store.getState().applySeed({ host: "local", cwd: "/project", model: "p/a", effort: "low" });
  answer(calls, "model/list", null, { data: [model] });
  await flush();
  expect(store.getState()).toMatchObject({ source: "local", cwd: "/project", model, reasoning: "low" });
});
```

  In `creationDraftRepository.test.ts`, add: a draft written with `source: "paradise-park"` reads back with it, and a stored draft without `source` (written as today's JSON) reads back with no `source`. Also in `newSession.test.ts`: a store restoring a draft without `source` has `source: "local"`, and one restoring `"paradise-park"` has that. Update any existing assertion on the draft's written shape to include `source: "local"`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/newSession.test.ts src/creationDraftRepository.test.ts`
- [ ] **Step 3: Implement.** The file uses two-space indentation; keep it.
  1. Imports: `LOCAL_HOST` from `../../cmd/evener-hub/frontend/src/stores/hostRouting`, and `type LaunchSetup`, `modelFromId`, `moveToHost`, `type SessionSeed` and `withOwnedOverrides` from `./newSession/launchSetup`.
  2. `creationModel` matches through `modelFromId`:

```ts
export function creationModel(
  models: ModelDescriptor[],
  selected: ModelDescriptor | null,
  overrides: LaunchConfigLayer,
): ModelDescriptor | null {
  const id = overrides.model?.trim();
  if (!id) return selected;
  return modelFromId(id, models);
}
```

  3. `Form` gains:

```ts
  /** The host the session starts on: "local" is the hub's own machine. */
  source: string;
  /** What moved when the host did (ruling 17), for the Host row's footer. */
  hostNote: string | null;
  changeHost(host: string, hostLabel: string): Promise<void>;
  applySetup(setup: LaunchSetup): void;
  applySeed(seed: SessionSeed): void;
```

  4. Beside the store's other closure variables:

```ts
  // Bumped by every host change and every applied setup, so a host change
  // whose answers arrive after a newer one drops them (Review Focus 2).
  let placement = 0;
  // A session's model named the way it reports it, waiting for the host's
  // model list to find it (applySeed).
  let pendingModelId: string | null = null;
```

  5. The initial state gains `source: LOCAL_HOST` and `hostNote: null`. The success reset in `submit` adds `source: LOCAL_HOST` and `hostNote: null`.
  6. The new methods:

```ts
    async changeHost(host, hostLabel) {
      const state = get();
      if (state.submitting || host === state.source) return;
      const current = service;
      const mine = ++placement;
      loadedContext = null;
      catalog++;
      refreshingModels = false;
      set({ source: host, hostNote: null, projects: [], models: [], loadingModels: false });
      if (!current) return;
      const cwd = state.cwd.trim();
      // A host that can't answer keeps the project: unknown isn't absent.
      const [exists, recent] = await Promise.all([
        cwd ? current.directoryExists(host, cwd).catch(() => null) : Promise.resolve(null),
        current.recentProjects(host).catch(() => [] as string[]),
      ]);
      if (mine !== placement || service !== current) return;
      const move = moveToHost(cwd, hostLabel, exists !== false, recent);
      set({ projects: recent, cwd: move.cwd, hostNote: move.note });
      await get().loadModels();
    },
    applySetup(setup) {
      if (get().submitting) return;
      placement++;
      pendingModelId = null;
      set({
        source: setup.host,
        cwd: setup.cwd,
        hostNote: null,
        model: setup.model ? { provider: setup.model.provider, model: setup.model.model } : null,
        reasoning: setup.effort,
        launchOverrides: withOwnedOverrides(get().launchOverrides, setup.overrides),
      });
      void get().loadModels(true);
    },
    applySeed(seed) {
      if (get().submitting) return;
      placement++;
      pendingModelId = seed.model ?? null;
      set({ source: seed.host, cwd: seed.cwd, hostNote: null, model: null, reasoning: seed.effort ?? "" });
      void get().loadModels(true);
    },
```

  7. `loadMetadata` asks `current.recentProjects(get().source)`.
  8. `loadModels` keys its context on the host: `const { cwd, harness, source } = get(); const context = JSON.stringify([source, cwd.trim(), harness]);`. It passes the host: `current.models({ …the same params… }, source)`. When the answer lands, it resolves the seed first:

```ts
          const seeded = pendingModelId === null ? null : modelFromId(pendingModelId, result.data);
          pendingModelId = null;
          const model =
            seeded ??
            result.data.find(
              (item) =>
                item.provider === selection?.provider &&
                item.model === selection.model,
            ) ??
            null;
```

  9. `submit` checks `loadedContext !== JSON.stringify([source, cwd.trim(), harness])`. It previews through `current.previewPlugins({ cwd: cwd.trim(), launchOverrides }, source)`, and adds `source` to the `current.start({ … })` call's params (the service leaves it out for the hub's own machine).
  10. `snapshot()` adds `source: state.source`. `restoreDraft` sets `source: draft.source ?? LOCAL_HOST`, because a draft saved before this phase has no host (ruling 28).
  11. In `creationDraftRepository.ts`, `CreationDraft` gains `source?: string`. `decode` also refuses a draft whose `source` is neither absent nor a string: `(value.source !== undefined && typeof value.source !== "string")`.
- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/newSession.test.ts src/creationDraftRepository.test.ts src/creationPlugins.test.ts && npm run check`
- [ ] **Step 5: Commit and open PR 8** (`feat(native): the New session's rules, memory and host-routed reads (phase 5, PR 8)`).

---

## PR 9: the New session sheet

PR 9 replaces today's New session screen with the sheet (spec 11). It includes host, project, model and effort. Plugins and the per-launch options keep today's choosers as interim rows until PR 10 (ruling 10's pattern). It starts once PRs 1, 5 and 8 are on main.

### Task 20: The sheet, its form, Start and Cancel, and the host picker

**Files:**
- Create:
  - `mobile-native/src/newSession/startGate.ts` (pure) and its test;
  - `mobile-native/src/newSession/NewSessionSheet.tsx` (the root route's component: its nested stack and context);
  - `mobile-native/src/newSession/NewSessionForm.tsx` and `mobile-native/src/newSession/HostPicker.tsx`.
- Modify:
  - `mobile-native/App.tsx`: `NewSession` renders `NewSessionSheet` with `presentation: "modal"` and `headerShown: false`.
  - `mobile-native/src/screens.tsx`: `NewSession: { hubId: string; hubName: string; like?: SessionSeed }`.
  - `mobile-native/src/newSession.ts`: the harness goes (ruling 13), and `discard()` joins.
  - `mobile/src/services/newSession.ts`: `harnesses()` and `NewSessionParams.harness` go.
  - `mobile-native/src/CreationPlugins.tsx`: a `host` prop passed to `usePluginPreview`, for its interim use.
- Delete: `mobile-native/src/NewSessionScreen.tsx` and `mobile-native/src/CreationComposerSettings.tsx`.
- Test:
  - `mobile-native/src/newSession/startGate.test.ts`;
  - `mobile-native/src/newSession/NewSessionForm.test.tsx` and `HostPicker.test.tsx`;
  - `mobile-native/src/newSession.test.ts` and `mobile/src/services/newSession.test.ts`, with the harness cases removed;
  - `mobile-native/src/demo-hub.test.ts` and `mobile-native/src/creationPlugins.test.ts`, which also call the harness API (item 8).

**Interfaces:**
- Consumes:
  - Tasks 1, 2, 10, 16, 17, 18 and 19;
  - `ImageAttachments`, `ImageSelection`, `creationImageDraft`, `nativeImagePicker` and `nativeDrafts`, as `NewSessionScreen` uses them today.
- Produces:
  - `startBlock(input: StartInput): StartBlock | null`, where `StartBlock = { field: "host" | "project" | "plugins" | null; message: string | null }`;
  - `hostReach(host, rows): "local" | "connected" | "offline" | "missing" | "pending"`;
  - `NewSessionContext`: the store, `hubId`, `hubName`, `client`, `ready`, `hosts: HostsController`, `live: LiveSessionsReader`, `memory: LaunchMemory`, and `hostLabel(host): string` (ruling 3);
  - `NewSessionRoutes = { Form: undefined; Host: undefined; Project: undefined; Browse: { dir: string }; Model: undefined; Plugins: undefined; SessionOptions: undefined }`. PR 10 replaces the last two.
  - The store gains `discard(): void`: it clears the saved draft and empties the form.

- [ ] **Step 1: Write the failing tests for the gate**

```ts
// mobile-native/src/newSession/startGate.test.ts
import { describe, expect, it } from "vitest";
import type { HostRow } from "@evener/appwire-client";
import { hostReach, type StartInput, startBlock } from "./startGate";

const ready: StartInput = {
	ready: true,
	busy: false,
	cwd: "/home/jesse/git/evener",
	host: "local",
	hostLabel: "magic-kingdom",
	reach: "local",
	pluginIssues: [],
};
const row = (over: Partial<HostRow>): HostRow => ({
	name: "paradise-park",
	origin: "hub.toml",
	attached: true,
	midAttach: false,
	removed: false,
	...over,
});

describe("where a host stands", () => {
	it("is local for the hub's own machine, and pending before the hub lists its hosts", () => {
		expect(hostReach("local", null)).toBe("local");
		expect(hostReach("paradise-park", null)).toBe("pending");
	});

	it("reads the hub's list for any other host", () => {
		expect(hostReach("paradise-park", [row({})])).toBe("connected");
		expect(hostReach("paradise-park", [row({ attached: false })])).toBe("offline");
		expect(hostReach("paradise-park", [row({ attached: false, midAttach: true })])).toBe("offline");
		expect(hostReach("paradise-park", [])).toBe("missing");
	});
});

describe("what keeps Start disabled", () => {
	it("is nothing when the form can start", () => {
		expect(startBlock(ready)).toBeNull();
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "connected" })).toBeNull();
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "pending" })).toBeNull();
	});

	it("waits quietly for the connection and for work in flight", () => {
		expect(startBlock({ ...ready, ready: false })).toEqual({ field: null, message: null });
		expect(startBlock({ ...ready, busy: true })).toEqual({ field: null, message: null });
	});

	it("never starts on an offline or missing host (Review Focus 3)", () => {
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "offline" })).toEqual({
			field: "host",
			message: "paradise-park is offline. Connect it or choose another host.",
		});
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "missing" })).toEqual({
			field: "host",
			message: "paradise-park is no longer a host on this hub. Choose another host.",
		});
	});

	it("asks for a project, quietly", () => {
		expect(startBlock({ ...ready, cwd: "  " })).toEqual({ field: "project", message: null });
	});

	it("names each plugin with a blocking problem", () => {
		expect(
			startBlock({
				...ready,
				pluginIssues: [
					{ name: "superpowers-chrome", reason: "needs Chrome on this host" },
					{ name: "gone", reason: "not present in current preview" },
				],
			}),
		).toEqual({
			field: "plugins",
			message: "superpowers-chrome: needs Chrome on this host\ngone: not present in current preview",
		});
	});
});
```

- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/newSession/startGate.test.ts`
- [ ] **Step 3: Implement the gate**

```ts
// mobile-native/src/newSession/startGate.ts
// Why Start is disabled, and which row says so (spec 11; rulings 20 and 21).
// Only what the phone itself checked blames a row; a hub rejection after
// Start is shown as the hub said it.
import type { HostRow } from "@evener/appwire-client";
import { LOCAL_HOST } from "../../../cmd/evener-hub/frontend/src/stores/hostRouting";

export type HostReach = "local" | "connected" | "offline" | "missing" | "pending";

/** Where the chosen host stands. Before the hub lists its hosts the answer is
 * pending, and pending never blocks: the hub checks the host at Start, as the
 * web learned not to guess from a list still in flight (Spawn.tsx,
 * submittedSource). */
export function hostReach(host: string, rows: readonly HostRow[] | null): HostReach {
	if (host === LOCAL_HOST) return "local";
	if (rows === null) return "pending";
	const row = rows.find((candidate) => candidate.name === host);
	if (!row) return "missing";
	return row.attached ? "connected" : "offline";
}

export interface StartInput {
	/** The connection is ready for this hub. */
	ready: boolean;
	/** Storage loading, a start or model read in flight, or images processing. */
	busy: boolean;
	cwd: string;
	host: string;
	hostLabel: string;
	reach: HostReach;
	/** The plugin preview's blocking problems (pluginSelectionIssues). */
	pluginIssues: readonly { name: string; reason: string }[];
}

export interface StartBlock {
	/** The row the reason belongs to, or null when no row is to blame. */
	field: "host" | "project" | "plugins" | null;
	/** The sentence to show, or null when the row or the status line already
	 * says it. */
	message: string | null;
}

export function startBlock(input: StartInput): StartBlock | null {
	if (!input.ready || input.busy) return { field: null, message: null };
	if (input.reach === "offline")
		return { field: "host", message: `${input.hostLabel} is offline. Connect it or choose another host.` };
	if (input.reach === "missing")
		return { field: "host", message: `${input.hostLabel} is no longer a host on this hub. Choose another host.` };
	if (!input.cwd.trim()) return { field: "project", message: null };
	if (input.pluginIssues.length > 0)
		return {
			field: "plugins",
			message: input.pluginIssues.map((issue) => `${issue.name}: ${issue.reason}`).join("\n"),
		};
	return null;
}
```

- [ ] **Step 4: Run them and watch them pass.** Run: `cd mobile-native && npx vitest run src/newSession/startGate.test.ts`
- [ ] **Step 5: Build the sheet to these requirements (spec 11; rulings 1, 13, 18, 19 and 21).**
  1. **The container (`NewSessionSheet`).**
     - It owns the store: `createNewSessionStore(hubId, () => nativeDrafts().creation)`, bound to `createNewSessionService(client)` while ready. It loads metadata and models on bind, as `NewSessionScreen` does today.
     - It owns a `HostsController` and a `LiveSessionsReader` per client, and `launchMemory(hubId)`.
     - It provides all of these through `NewSessionContext` to a nested native stack whose first screen is `Form`. The stack's screen options are HubSheet's.
  2. **Opening.** On first mount, with the draft loaded:
     - With `like`: `applySeed(like)` once.
     - Else, with a draft that already has a project or a prompt: the draft stands.
     - Else, with a remembered start: `applySetup(newestSetup(memory.history()))`.
     - Else: once the hub's recent projects arrive, the most recent becomes the project.
  3. **The form's header:**
     - "Cancel" on the left;
     - the title "New session";
     - "Start" on the right: semibold accent, `inkLow` and inert while `startBlock` returns a block.
  4. **The form's body**, top to bottom:
     - `SheetStatus`.
     - The prompt: SF Pro 17, placeholder "What should the agent do?", focused on open, growing to six lines and then scrolling, with today's image "+" and thumbnails (`ImageAttachments`).
     - The Start block's message as a danger `GroupFooter`, when it has one. The store's `error` (a hub rejection) shows the same way.
     - "WHERE":
       - "Host": `server.rack`, valued with `hostLabel(source)`, plus an amber `Tag` "Offline" when `hostReach` says offline, and a chevron to `Host`. The row turns danger-tinted when the block's field is `host`. Its footer is `hostNote`.
       - "Project": `folder`, valued with `projectName(cwd)` or "Choose a project", and a chevron to `Project`. It turns danger-tinted when the block's field is `project`.
     - "AGENT":
       - "Model": `cpu`, second line "via <provider>", valued with the model's `displayName` (or its id), or "Hub default" with no second line. A chevron to `Model`.
       - "Effort" only when the chosen model lists `reasoningEffortLevels`: second line "How long it thinks before acting", then a `Segmented` of those levels labelled with `effortLabel`, lit on `reasoning` (none lit for ""). Choosing calls `setReasoning`.
       - Interim until PR 10: "Plugins" opens today's `CreationPlugins` chooser with `host={source}`, and "Session options" pushes `SessionOptions`, a page that renders today's `LaunchOverrides` open.
     - The footer: "Host, plugins and access are fixed once the session starts. Model and effort can change later."
  5. **Start.**
     - Keep `NewSessionScreen.submit`'s guards: ready, the same client, focused.
     - Read `setupOf(form)` before calling `submit()`.
     - On `created`, call `memory.recordStart(setup, Date.now())`, then replace the sheet with the session: `navigation.getParent()?.dispatch(StackActions.replace("Conversation", { hubId: outcome.hubId, ref: outcome.thread.evener.ref, title: outcome.thread.name || "Conversation" }))`.
     - Otherwise the sheet stays, with the store's error shown (ruling 20).
  6. **Cancel (ruling 18).**
     - With a prompt or an image, it asks `Alert.alert("Delete this draft?", undefined, [{ text: "Keep draft", style: "cancel", onPress: close }, { text: "Delete draft", style: "destructive", onPress: () => { store.getState().discard(); close(); } }])`.
     - Without either, it closes at once.
     - Swiping down closes and keeps the draft.
     - `close` is `navigation.getParent()?.goBack()`.
  7. **The host picker (`Host`, title "Host").**
     - `hosts.start()` runs while focused, and `live.load()` on focus.
     - The first row is the hub's own machine: `hostLabel("local")`, with the second line `${systemLabel}` if known and "N live". Then one row per `HostRow`.
     - The chosen row is checked. A connected row's second line joins its system and live count; an offline row's second line is its status word.
     - Offline rows can't be chosen. When `canConnect`, they carry a trailing accent "Connect" (a 44pt `Pressable` as the row's `value`), reading "Connecting…" while in flight.
     - Choosing a host calls `changeHost(name, hostLabel(name))` and pops.
  8. **The store (`src/newSession.ts`).**
     - Delete `harness`, `harnesses` and `setHarness`, and stop loading harnesses in `loadMetadata`. `loadModels`' context and params and `submit`'s `thread/start` params lose the harness.
     - `submit`'s `harnessSupportsPluginSelection(harness, get().harnesses)` check (about line 321) goes, and `submit` uses `get().launchOverrides` as it is. With no harness chosen the check was always true: the default harness is evener (`harnessUsesEvenerModels`, `appwire-client/typescript/spawnHarnessModels.ts`).
     - `snapshot()` keeps writing `harness: ""`, so the draft's stored shape doesn't change (ruling 13).
     - `discard()` calls `storage().clear(hubId)` and resets the fields `submit`'s success resets.
     - The failure before anything was sent (the plugin preview rejecting, so `startDispatched` is still false; `newSession.ts` around line 423) reads "Couldn't check the selected plugins, so no session was started. Your selection is kept." Today's sentence ends "Reconnect or retry; your selection is kept.", which asks you to reconnect.
     - In `mobile/src/services/newSession.ts`, delete `harnesses()` and `NewSessionParams.harness`, with their tests.
     - Two more tests call that API, and both must change in this task, or PR 9's `npm run check` and tests fail:
       - `demo-hub.test.ts` (about line 124): the creation case stops calling `creation.harnesses()`, drops `harness` from its `models` and `start` calls, and loses `expect(harness).toBe("demonstration")`.
       - `creationPlugins.test.ts` (about line 88): delete the case "omits plugin selection for an unsupported harness". It drives `setHarness("codex")`, and nothing on the phone chooses a harness any more. Say so in the commit.
     - `grep -rn "setHarness\|harnesses()\|harnessSupportsPluginSelection" mobile-native/src mobile/src` then finds nothing outside `NewSessionScreen.tsx`, which this task deletes.
  9. **No harness, thread, source or runtime** appears on screen, and no "Reconnect".
- [ ] **Step 6: Write the failing tests for the sheet.**
  - `NewSessionForm.test.tsx` renders the form inside `NewSessionContext` with a real store over a scripted client, a `HostsController` over the same client, and a memory-backed `LaunchMemory`. Mocks: `react-native`, `expo-symbols`, `@react-navigation/native` (`StackActions`), `../nativeDrafts`, `../nativeImagePicker`. Cases:
    - the header and rows with their values;
    - "Hub default" with no Effort row, then Effort's levels ("Low", "Med", "High", "XHigh", "Max") once a model with those levels is chosen;
    - **Review Focus 3:** with the draft's `source` "paradise-park" and `evener/host/list` answering it with `attached: false`, Start is inert, the Host row carries "Offline", the sheet says "paradise-park is offline. Connect it or choose another host.", and pressing Start sends no `thread/start`. The same with the host missing from the list: "…is no longer a host on this hub. Choose another host.";
    - Start sends `thread/start` with `source` for another host (Review Focus 1), records the start in the memory, and replaces the route with `Conversation`;
    - a rejected start keeps the sheet and shows the hub's message;
    - **Review Focus 4:** with the connection dropped, the typed prompt stays, Start is inert, and no text matches `/\bReconnect\b/`;
    - Cancel with a prompt asks "Delete this draft?", and "Delete draft" empties the draft; Cancel with nothing closes at once;
    - opening with `like` applies the seed; opening with a remembered start applies it; opening with neither takes the most recent project.
  - `HostPicker.test.tsx`: rows, checks, the disabled offline row with "Connect" calling `evener/host/attach`, and choosing a host calling `changeHost` and popping.
  - `newSession.test.ts` gains a case: after `setLaunchOverrides({ enabledPlugins: ["superpowers"] })`, with `evener/plugin/preview` rejecting, `submit()` returns `{ status: "failed" }`, sends no `thread/start`, keeps `enabledPlugins`, and sets `error` to exactly "Couldn't check the selected plugins, so no session was started. Your selection is kept."
- [ ] **Step 7: Run them and watch them fail**, then implement, then run them and watch them pass. Run:
  - `cd mobile-native && npx vitest run src/newSession src/newSession.test.ts src/demo-hub.test.ts src/creationPlugins.test.ts && npm run check && make test-native-bundle`
  - `npm run test:shared -- newSession`
- [ ] **Step 8: Commit** (`feat(native): the New session sheet`).

### Task 21: Choosing the project and the model

**Files:**
- Create:
  - `mobile-native/src/newSession/ProjectPicker.tsx` and `mobile-native/src/newSession/BrowseFolders.tsx`;
  - `mobile-native/src/newSession/ModelPicker.tsx`;
  - `mobile-native/src/newSession/modelFacts.ts` (pure).
  - `mobile-native/src/newSession/ModelList.tsx`, only if phase 3 left no reusable model list ("Built on earlier phases").
- Modify: `mobile-native/src/newSession.ts`: the store keeps `recentModels` from `model/list`'s `recent`.
- Test: `mobile-native/src/newSession/modelFacts.test.ts`, `ProjectPicker.test.tsx`, `BrowseFolders.test.tsx`, `ModelPicker.test.tsx`; one case in `mobile-native/src/newSession.test.ts`.

**Interfaces:**
- Consumes:
  - `HubPaths` with a host (Task 18);
  - `buildPathRows`, `childrenPrefix` and `parentOf` from `@evener/appwire-client`;
  - the service's `createDirectory`.
- Produces:
  - `contextText(tokens?: number): string | null` ("200K", "1M");
  - `priceText(input?: number, output?: number): string | null` ("$0.60 / $2.20 per M", "Free");
  - `modelFacts(model: ModelDescriptor): string`;
  - the store's `recentModels: ModelDescriptor[]`.

- [ ] **Step 1: Write the failing tests for the model facts**

```ts
// mobile-native/src/newSession/modelFacts.test.ts
import { expect, it } from "vitest";
import { contextText, modelFacts, priceText } from "./modelFacts";

it("sizes a context window compactly (spec 5)", () => {
	expect(contextText(200_000)).toBe("200K");
	expect(contextText(128_000)).toBe("128K");
	expect(contextText(1_000_000)).toBe("1M");
	expect(contextText(1_048_576)).toBe("1M");
	expect(contextText(undefined)).toBeNull();
});

it("prices input and output per million tokens", () => {
	expect(priceText(0.6, 2.2)).toBe("$0.60 / $2.20 per M");
	expect(priceText(1.25, 10)).toBe("$1.25 / $10 per M");
	expect(priceText(0, 0)).toBe("Free");
	expect(priceText(undefined, 2)).toBeNull();
});

it("puts a model's facts on one line", () => {
	expect(
		modelFacts({
			provider: "lunaroute",
			model: "glm-5.3-vision",
			contextWindow: 200_000,
			inputCostPerMillion: 0.6,
			outputCostPerMillion: 2.2,
		}),
	).toBe("lunaroute · 200K context · $0.60 / $2.20 per M");
	expect(modelFacts({ provider: "ollama", model: "qwen3-coder:30b" })).toBe("ollama");
});
```

- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/newSession/modelFacts.test.ts`
- [ ] **Step 3: Implement the facts**

```ts
// mobile-native/src/newSession/modelFacts.ts
// A model row's second line in the model picker (spec 11: provider profile,
// context size, price per million tokens). Numbers are compact (spec 5).
import type { ModelDescriptor } from "@evener/appwire-client";

export function contextText(tokens?: number): string | null {
	if (!tokens || tokens <= 0) return null;
	if (tokens >= 1_000_000) return `${Math.round(tokens / 1_000_000)}M`;
	return `${Math.round(tokens / 1_000)}K`;
}

function dollars(value: number): string {
	return Number.isInteger(value) ? `$${value}` : `$${value.toFixed(2)}`;
}

export function priceText(input?: number, output?: number): string | null {
	if (input === undefined || output === undefined) return null;
	if (input === 0 && output === 0) return "Free";
	return `${dollars(input)} / ${dollars(output)} per M`;
}

export function modelFacts(model: ModelDescriptor): string {
	const context = contextText(model.contextWindow);
	return [model.provider, context ? `${context} context` : null, priceText(model.inputCostPerMillion, model.outputCostPerMillion)]
		.filter((part): part is string => !!part)
		.join(" · ");
}
```

- [ ] **Step 4: Run them and watch them pass**, then build the pickers to these requirements (spec 11; rulings 1 and 17):
  1. **The store keeps the host's recent models.** `loadModels` also sets `recentModels: result.recent ?? []`, and every place that empties `models` empties `recentModels` too. Test it with one case in `newSession.test.ts`.
  2. **The project picker (`Project`, title "Project"):**
     - A search field, "Projects on <host label>", filters by name and path.
     - "RECENT ON <HOST LABEL>" lists `projects`: the name, the path as a Menlo second line, and a check on the chosen one. Tapping one calls `setCwd(path, true)` and pops.
     - An accent row, "Browse folders on <host label>…", pushes `Browse` with `{ dir: parentOf(cwd) }`, or `{ dir: "" }` (home) with no project.
  3. **Browsing folders (`Browse`):**
     - The rows come from `new HubPaths(client, false, source)` and `buildPathRows({ kind: "dir", currentDir: dir, entries, value: cwd, recents: [], showRecents: false, listError })`:
       - `group` is a Menlo `GroupLabel` (the folder, or "Home");
       - `parent` is a row "Up one folder", replacing `dir` with `parentOf(dir)`;
       - `dir` rows are folders (`folder` icon, chevron), each replacing `dir` with its path;
       - `status` rows are quiet text.
     - It stays one page whose `dir` changes in place, so Back leaves the browser at once.
     - An accent row, "Use this folder", calls `setCwd(dir, true)` and pops to the form. It is hidden while `dir` is `""`, because the phone doesn't know which folder home resolves to.
     - A row, "New folder", asks `Alert.prompt("New folder", \`In ${dir || "your home folder"}\`, …)`, then calls `createDirectory(source, childrenPrefix(dir) + name)` and moves into the folder it made. The hub's refusal shows as a danger footer.
  4. **The model picker (`Model`, title "Model"):**
     - If phase 3 built a model list for the composer's model sheet, reuse that list (not the `ModelSheet` route, which reads a session's host; ruling 27) with no effort control. Otherwise build `ModelList`:
       - a search field;
       - a first row, "Hub default", checked when no model is chosen;
       - "RECENT" with up to five `recentModels`;
       - then one group per provider, labelled as typed in Menlo.
     - Each model row shows:
       - the display name (or the model id);
       - `modelFacts` as its second line;
       - a trailing `eye` glyph when `supportsVision` and `wrench.and.screwdriver` when `supportsTools`, both `inkMid` and each with an accessibility label ("Sees images", "Uses tools");
       - `warnings` as an attention-ink line;
       - a check on the chosen model.
     - Choosing calls `selectModel(model)` and pops.
     - There is no effort control: spec 11 keeps effort in one place, the form.
     - While models load, the rows are quiet and the form's Start stays inert (`loadingModels`).
- [ ] **Step 5: Write the pickers' failing tests.**
  - `ProjectPicker.test.tsx`:
    - the recent list and its filter;
    - choosing sets the project and pops;
    - Browse pushes with the chosen project's parent.
  - `BrowseFolders.test.tsx`, with a scripted client answering `evener/host/request` for `paradise-park`:
    - entering a folder and going up;
    - "Use this folder";
    - "New folder" creating through `evener/dirs/create` and moving in (the kit's Alert mock gains `prompt`, recording its callback);
    - no "Use this folder" at home.
  - `ModelPicker.test.tsx`:
    - "Hub default" first;
    - recent before the provider groups;
    - the second line;
    - the capability glyphs;
    - choosing selects and pops;
    - the list filters.
- [ ] **Step 6: Run them and watch them fail**, then implement, then run them and watch them pass. Run: `cd mobile-native && npx vitest run src/newSession src/newSession.test.ts src/hubPaths.test.ts && npm run check`
- [ ] **Step 7: Look in the simulator.** Start a session on the hub's own machine and on a second host. Browse into a folder on the second host, make a new folder there, and start in it.
- [ ] **Step 8: Commit and open PR 9** (`feat(native): the New session sheet (phase 5, PR 9)`).

---

## PR 10: plugins, access and options in New session

### Task 22: The plugin checklist, access, branch and More options

**Files:**
- Create:
  - `mobile-native/src/newSession/pluginFacts.ts` (pure);
  - `mobile-native/src/newSession/PluginChecklist.tsx`, `mobile-native/src/newSession/AccessPicker.tsx` and `mobile-native/src/newSession/MoreOptions.tsx`;
  - `mobile-native/src/newSession/useLaunchDefaults.ts`.
- Modify: `mobile-native/src/newSession/NewSessionSheet.tsx` and `NewSessionForm.tsx`. Routes `Plugins`, `Access` and `MoreOptions` replace the interim `Plugins` and `SessionOptions`.
- Delete: `mobile-native/src/CreationPlugins.tsx` and `mobile-native/src/LaunchOverrides.tsx`, whose last user was the interim rows. Move `creationPlugins.test.ts`'s cases that still describe behavior to `PluginChecklist.test.tsx`.
- Test: `mobile-native/src/newSession/pluginFacts.test.ts`, `PluginChecklist.test.tsx`, `AccessPicker.test.tsx`, `MoreOptions.test.tsx` and `useLaunchDefaults.test.ts`; more cases in `NewSessionForm.test.tsx`.

**Interfaces:**
- Consumes:
  - `usePluginPreview` (`cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.ts`: `{ client, cwd, host, launchOverrides, pluginRevision, enabled }`);
  - `pluginSelectionFromOverrides`, `selectedPluginNames`, `setPluginSelected`, `selectAllPlugins`, `selectNoPlugins`, `withPluginSelection` and `pluginSelectionIssues` from `@evener/appwire-client`;
  - Task 16's `ACCESS_LEVELS`, `accessOf` and `networkApplies`;
  - the service's `branch` and `resolveLaunch`.
- Produces:
  - `pluginCounts(plugin): string`;
  - `pluginGroups(preview): { marketplace: string | null; plugins: PluginLaunchCandidate[] }[]`;
  - `pluginWarnings(preview, name): string[]`;
  - `useLaunchDefaults(host, cwd): LaunchDefaults | null`, where `LaunchDefaults = Pick<LaunchConfigLayer, "sandbox" | "sandboxNet" | "contextStrategy" | "maxSubagentDepth" | "maxRounds">`.

- [ ] **Step 1: Write the failing tests for the plugin facts**

```ts
// mobile-native/src/newSession/pluginFacts.test.ts
import { expect, it } from "vitest";
import type { PluginLaunchCandidate, PluginPreviewResponse } from "@evener/appwire-client";
import { pluginCounts, pluginGroups, pluginWarnings } from "./pluginFacts";

const plugin = (name: string, marketplace?: string, over: Partial<PluginLaunchCandidate> = {}): PluginLaunchCandidate => ({
	name,
	source: marketplace ? "installed" : "dir",
	...(marketplace ? { marketplace } : {}),
	selected: true,
	skillCount: 0,
	agentCount: 0,
	commandCount: 0,
	hookCount: 0,
	mcpCount: 0,
	...over,
});

it("counts what a plugin brings, leaving out what it doesn't (spec 11)", () => {
	expect(pluginCounts(plugin("superpowers", "superpowers-marketplace", { skillCount: 38, agentCount: 3, commandCount: 6, hookCount: 2 }))).toBe(
		"38 skills · 3 agents · 6 commands · 2 hooks",
	);
	expect(pluginCounts(plugin("private-journal-mcp", "superpowers-marketplace", { mcpCount: 1 }))).toBe("1 MCP server");
	expect(pluginCounts(plugin("empty"))).toBe("");
});

it("groups plugins by marketplace in the order the hub lists them", () => {
	const preview: PluginPreviewResponse = {
		plugins: [plugin("superpowers", "superpowers-marketplace"), plugin("go", "go-skills"), plugin("local-tool"), plugin("elements-of-style", "superpowers-marketplace")],
	};
	expect(pluginGroups(preview).map((group) => [group.marketplace, group.plugins.map((p) => p.name)])).toEqual([
		["superpowers-marketplace", ["superpowers", "elements-of-style"]],
		["go-skills", ["go"]],
		[null, ["local-tool"]],
	]);
});

it("finds a plugin's own warnings", () => {
	const preview: PluginPreviewResponse = {
		plugins: [plugin("superpowers-chrome", "superpowers-marketplace")],
		diagnostics: [
			{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" },
			{ message: "a marketplace failed to refresh" },
		],
	};
	expect(pluginWarnings(preview, "superpowers-chrome")).toEqual(["Chrome isn't installed on this host"]);
	expect(pluginWarnings(preview, "go")).toEqual([]);
});
```

- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/newSession/pluginFacts.test.ts`
- [ ] **Step 3: Implement the facts**

```ts
// mobile-native/src/newSession/pluginFacts.ts
// A plugin row's facts in the New session checklist (spec 11): what it brings,
// its marketplace group, and its preview warnings.
import type { PluginLaunchCandidate, PluginPreviewResponse } from "@evener/appwire-client";

const COUNTS = [
	["skillCount", "skill", "skills"],
	["agentCount", "agent", "agents"],
	["commandCount", "command", "commands"],
	["hookCount", "hook", "hooks"],
	["mcpCount", "MCP server", "MCP servers"],
] as const;

export function pluginCounts(plugin: PluginLaunchCandidate): string {
	return COUNTS.filter(([field]) => plugin[field] > 0)
		.map(([field, one, many]) => `${plugin[field]} ${plugin[field] === 1 ? one : many}`)
		.join(" · ");
}

/** Plugins grouped by marketplace, groups in the order their first plugin
 * appears; plugins from no marketplace (a plugin directory) group under null. */
export function pluginGroups(
	preview: PluginPreviewResponse,
): { marketplace: string | null; plugins: PluginLaunchCandidate[] }[] {
	const groups = new Map<string | null, PluginLaunchCandidate[]>();
	for (const plugin of preview.plugins) {
		const key = plugin.marketplace ?? null;
		const group = groups.get(key);
		if (group) group.push(plugin);
		else groups.set(key, [plugin]);
	}
	return [...groups].map(([marketplace, plugins]) => ({ marketplace, plugins }));
}

export function pluginWarnings(preview: PluginPreviewResponse, name: string): string[] {
	return (preview.diagnostics ?? []).filter((diagnostic) => diagnostic.name === name).map((diagnostic) => diagnostic.message);
}
```

- [ ] **Step 4: Run them and watch them pass**, then build to these requirements (spec 11; rulings 14 and 15):
  1. **`useLaunchDefaults(host, cwd)`.**
     - It reads `service.resolveLaunch(host, cwd, {})` whenever the host or project changes, ignoring answers for a host or project the form has left.
     - It returns the effective `sandbox`, `sandboxNet`, `contextStrategy`, `maxSubagentDepth` and `maxRounds`: the hub's defaults for that project, since the sheet's own overrides are left out.
     - It returns null before the first answer and after a failure. The pages then say "the hub's default" without a value.
  2. **The plugin checklist (`Plugins`, title "Plugins").**
     - The preview comes from `usePluginPreview({ client, cwd, host: source, launchOverrides, pluginRevision, enabled: !!cwd })`. `pluginRevision` bumps on `evener/plugin/updated` and `evener/launch/updated`, as `CreationPlugins` did.
     - Under a search field ("Search plugins"), a row with accent text buttons "All" (`selectAllPlugins`) and "None" (`selectNoPlugins`).
     - Groups from `pluginGroups`: each labelled with its marketplace as typed (`GroupLabel machine`), or "Other plugins".
     - Each row shows the name, the description as its second line, `pluginCounts` as a third line in ink-low, `pluginWarnings` in attention ink, and a blocking `pluginSelectionIssues` entry in danger ink. Its trailing switch is on when the name is in `selectedPluginNames`. Flipping it writes `withPluginSelection(launchOverrides, setPluginSelected(selection, preview, name, on))`.
     - The footer: "<n> of <total> on. Plugins can't be changed after the session starts."
     - While the first preview loads, one quiet line "Checking plugins on <host label>…". A failed preview shows its message with no button; it re-reads on the next change.
  3. **The form's Plugins row** (`puzzlepiece.extension`):
     - valued "<n> of <total>", or "…" while loading;
     - its second line "<k> need attention" in danger ink when issues block, which also feed `startBlock`'s `pluginIssues`.
  4. **Access (`Access`, title "Access").**
     - The four `ACCESS_LEVELS` as checked rows: the label, with the detail as the second line. The check sits on `accessOf(launchOverrides.sandbox, defaults?.sandbox)`.
     - Choosing writes `sandbox`. Choosing the level the hub's default already is removes the override, so the setup keeps following the hub.
     - When `networkApplies`, a `SwitchRow` "Network", second line "Lets the session's commands reach the internet", on when `launchOverrides.sandboxNet ?? defaults?.sandboxNet ?? true`. Flipping it writes `sandboxNet`.
     - The footer: "Access is fixed once the session starts."
     - The form's Access row (`lock.shield`) is valued with the label, with the detail as its second line.
  5. **Branch** sits in the WHERE group after Project:
     - `arrow.triangle.branch`, labelled "Branch", valued with `service.branch(source, cwd)`: the current branch, "main" in the spec's frame;
     - no chevron (ruling 15);
     - hidden when the folder isn't a repository (null) or while unknown.
  6. **More options (`MoreOptions`, title "More options").**
     - Three groups, each a `Segmented` whose first segment "Default" removes the field:
       - "CONTEXT STRATEGY": Default, compact, session-log, ooda;
       - "MAX SUBAGENT DEPTH": Default, 1, 2, 3, 5;
       - "MAX TURNS": Default, 100, 500, No limit (−1).
     - Under each, the hub's default from `useLaunchDefaults`: "The hub's default is compact.", "The hub's default is 2.", "The hub's default is no limit." (for −1).
     - A value that isn't a segment (a remembered start's 4) lights no segment, and the footer says "This session uses 4. The hub's default is 2."
     - The page's last footer: "Everything else uses the hub's launch defaults. Edit them from the Hub, under Launch defaults." (ruling 12 keeps that screen on the phone).
     - The form's row: "More options", second line "Context strategy, subagent depth, turn limit", valued "Custom" when any of the three is set.
- [ ] **Step 5: Write the failing tests**, one per requirement above. Cover:
  - `useLaunchDefaults` dropping a stale answer;
  - the checklist's All, None and toggle writing the right `enabledPlugins`, its footer count, and a blocking issue reaching the form's row and Start's message;
  - Access removing the override when the hub's default is chosen, and Network showing only inside a sandbox;
  - Branch hidden outside a repository;
  - More options' three groups and footers, and "Default" removing the field.
- [ ] **Step 6: Run them and watch them fail**, then implement, then run them and watch them pass. Run: `cd mobile-native && npx vitest run src/newSession && npm run check && make test-native-bundle`
- [ ] **Step 7: Commit and open PR 10** (`feat(native): plugins, access and options in New session (phase 5, PR 10)`).

---

## PR 11: New session like this

### Task 25: New session like this

**Files:**
- Modify: the session screen's ⋯ menu (phase 3's module for spec 8.7; read the landed code) and its test.

**Requirements (spec 11; ruling 24):**
1. The ⋯ menu gains "New session like this", placed after "Ask aside…".
2. It calls `navigation.navigate("NewSession", { hubId, hubName, like })`. The `like` seed is:
   - `host`: `hostOfRef(ref)`;
   - `cwd`: the thread's `cwd`;
   - `model`: the thread's `modelProvider`, when it isn't empty;
   - `effort`: `evener.reasoningEffort`, when set.

   All of these come from the thread read the session screen already holds.
3. The sheet applies the seed once (Task 20). Its plugins and access come from the newest remembered start, because a session's plugins and access aren't on the wire.

- [ ] **Step 1: Write the failing test** in the menu's test file: choosing the item navigates to `NewSession` with the seed built from a fixture thread on `paradise-park`.
- [ ] **Step 2: Run it and watch it fail**, then implement, then run it and watch it pass, plus `npm run check`.
- [ ] **Step 3: Commit and open PR 11** (`feat(native): New session like this (phase 5, PR 11)`).

---

## PR 12: the demo hub serves New session and the Hub

### Task 26: Demo answers for launching and for the Hub

Phase 2's demo fleet (`mobile-native/src/dev/demoFleet.ts`, PR #2471, on main) answers the Board's reads. The screenshots need the New session and Hub reads answered the same way, from the prototype's `data.js`, which is the spec's canonical fixture (Appendix B).

**Files:**
- Create: `mobile-native/src/dev/demoSetup.ts`
- Modify:
  - `mobile-native/src/dev/demoFleet.ts`: export `PLUGINS` and `EXPIRED_PROVIDER` (about lines 387 and 407), rather than copying them.
  - `mobile-native/scripts/demo-hub.mts`: route the new methods.
- Test: `mobile-native/src/dev/demoSetup.test.ts`

**Requirements (Appendix A frames 20-24, Appendix B):**
1. **With `EVENER_DEMO_FLEET=1`**, the demo hub answers these from the fleet. `model/list`, `evener/projects/recent` and `thread/start` replace today's one-session answers (`scripts/demo-hub.mts`, about lines 138-215); the rest are new:
   - **Hosts and updates:**
     - `evener/host/list`: paradise-park from `hub.toml`, attached, `darwin`/`arm64`, `hubVersion` "0.9.409", roots `["/Users/jesse/git"]`.
     - `evener/host/attach`: sets the host attached and answers `HostAttachResponse`.
     - `evener/update/check`: version "0.9.412", release channel, applicable, no update.
   - **Providers:**
     - `evener/instance/list`: the prototype's eleven providers as `InstanceEntry` rows, with their `authModes`, `activeSource` and model ids. lunaroute is the default.
     - `evener/auth/list`: extends PR B's answer with every OAuth instance, `codex-jesse-fsck.com` with `needsLogin`.
   - **Plugins and marketplaces:**
     - `evener/marketplace/list`: the prototype's five marketplaces.
     - `evener/marketplace/browse`: a three-plugin catalog for Browse.
     - `evener/plugin/preview`: the prototype's plugins as `PluginLaunchCandidate`s. Counts come from each plugin's `counts` text in `data.js` ("38 skills · 3 agents · 6 commands · 2 hooks"). `PLUGINS` leaves them out, so `demoSetup.ts` keeps them in a map keyed by plugin id. `selected` follows `on`, or the request's explicit `enabledPlugins`. `superpowers-chrome` on the hub's own machine carries the diagnostic "Chrome isn't installed on this host".
   - **Models:** `model/list`: the prototype's fifteen models as `ModelDescriptor`s. `contextWindow` is the prototype's `ctx` × 1000, the prices come from `pin`/`pout`, `supportsVision` from `vision`, `supportsTools` is true, and `reasoningEffortLevels` come from `efforts`. `recent` is deepseek-4.1-flash, glm-5.3-vision and gpt-5.6.
   - **Launching:**
     - `evener/projects/recent`: the prototype's projects under `/home/jesse/git/…`.
     - `evener/paths/complete`, `evener/path/validate` and `evener/dirs/create`: a small in-memory folder tree under `/home/jesse` that includes those projects.
     - `evener/git/head`: "main" inside a project, "" elsewhere.
     - `evener/launch/resolve`: `effective` `{ sandbox: "workspace-write", sandboxNet: true, contextStrategy: "compact", maxSubagentDepth: 2, maxRounds: -1 }`, with empty `layers` and `provenance`.
     - `evener/host/request { host: "paradise-park", method, params }`: the same launch answers, from paradise-park's own tree under `/Users/jesse/git` and its own recent projects. Any method outside the spawn form's forwarded list (`cmd/evener-hub/host_request_methods.txt`) answers with an error.
     - `thread/start` with a `source` starts the demo session as today, with a ref qualified by that source.
2. **`EVENER_DEMO_FLEET_OFFLINE_HOST=1`** marks paradise-park unattached, with `lastAttachError` "ssh: connect to host paradise-park port 22: Operation timed out" and its last-known `hubVersion` kept. That is frame 24.
3. **Without `EVENER_DEMO_FLEET`**, nothing changes.
4. **The tests:**
   - Every answer satisfies its generated wire type, checked by `npm run check`.
   - The offline variant.
   - `evener/host/request` answering paradise-park's projects, and refusing a method off the list.
   - The preview honoring an explicit selection.
   - `model/list` carrying fifteen models and three recent ones.

- [ ] Steps: write the failing tests, implement, then run `cd mobile-native && npx vitest run src/dev && npm run check`. Run the demo hub (`EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`), point a Release simulator build at port 9196, and walk New session and the Hub. Commit (`feat(native): the demo hub serves New session and the Hub`) and open PR 12: "feat(native): demo data for New session and the Hub (phase 5, PR 12)".

---

## PR 13: the last Reconnect, DESIGN.md, and screenshots

### Task 27: No screen asks you to reconnect

**Files:**
- Create: `mobile-native/src/noReconnect.test.ts`
- Modify:
  - `mobile-native/src/PinAssignmentScreen.tsx` and `mobile-native/src/PinAssignmentEditor.tsx`;
  - `mobile-native/src/retainedScreen.tsx` (`HUB_NO_LONGER_SELECTED`);
  - `mobile-native/src/connectionRecovery.ts`;
  - the administration screens ruling 12 keeps: `KeybindingPreferencesScreen.tsx`, `LaunchSettingsScreen.tsx`, `LaunchModelPicker.tsx`, `LaunchFallbackEditor.tsx`, `LaunchScalarEditor.tsx`, `LaunchResourceEditor.tsx` and `HubSettingsScreen.tsx`;
  - every other file the guard names.
- Delete: `mobile-native/src/ConnectionStatus.tsx`, and `ConnectionWall` and `ModalConnectionStatus` in `retainedScreen.tsx`, once nothing imports them (`grep -rn "ConnectionStatus\b\|ConnectionWall\|ModalConnectionStatus" mobile-native/src`).
- Test: the guard, and the touched screens' tests.

Today's administration screens stay on the phone (ruling 12), so the guard names them too, and Step 3 retires their Reconnect copy like any other file's.

- [ ] **Step 1: Write the guard**

```ts
// mobile-native/src/noReconnect.test.ts
// Spec principle 2 (Calm): the app reconnects on its own (hubConnection.ts),
// so no screen carries a Reconnect button or asks you to reconnect. This guard
// reads every production module under src/ and fails on shown text (a string
// literal or JSX text) that says "reconnect" as a word. "Reconnecting…" is
// the status line's word for what the app is doing, and stays.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const SRC = fileURLToPath(new URL(".", import.meta.url));

/** Modules a later phase owns, with why they may still ask. */
const LATER: Record<string, string> = {
	"nativeMutationHost.ts": "phase 6 rewrites the outbox's copy (spec 14)",
};

function productionSources(dir: string): string[] {
	return readdirSync(dir).flatMap((name) => {
		const path = join(dir, name);
		if (statSync(path).isDirectory()) return productionSources(path);
		return /\.tsx?$/.test(name) && !/\.test\.|\.testkit\./.test(name) ? [path] : [];
	});
}

/** The text a module can show: its string literals and JSX text, comments
 * removed first. A "//" right after ":" is a URL, not a comment. */
function shownText(source: string): string[] {
	const code = source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1");
	const strings = [...code.matchAll(/(["'`])((?:\\.|(?!\1)[^\\])*)\1/g)].map((match) => match[2] ?? "");
	const jsx = [...code.matchAll(/>([^<>{}]+)</g)].map((match) => match[1] ?? "");
	return [...strings, ...jsx];
}

const asksToReconnect = (text: string) => /\breconnect\b/i.test(text);

it("tells a request to reconnect apart from everything else", () => {
	expect(shownText(`<Action onPress={retry}>Reconnect</Action>`).some(asksToReconnect)).toBe(true);
	expect(shownText(`const a = "Could not load. Reconnect or retry.";`).some(asksToReconnect)).toBe(true);
	expect(shownText(`const b = "Update them together, then reconnect.";`).some(asksToReconnect)).toBe(true);
	expect(shownText(`const c = "Reconnecting…";`).some(asksToReconnect)).toBe(false);
	expect(shownText(`// Reconnects restore the proposal\nconst d = reconcileAfterReconnect;`).some(asksToReconnect)).toBe(false);
	expect(shownText(`const e = "https://hub.example.com:9180"; // Reconnect later`).some(asksToReconnect)).toBe(false);
});

it("no screen asks you to reconnect (spec principle 2)", () => {
	const offenders = productionSources(SRC)
		.map((path) => relative(SRC, path))
		.filter((file) => !(file in LATER))
		.filter((file) => shownText(readFileSync(join(SRC, file), "utf8")).some(asksToReconnect));
	expect(offenders).toEqual([]);
});
```

- [ ] **Step 2: Run it and watch it fail.** Run: `cd mobile-native && npx vitest run src/noReconnect.test.ts`. Expected: FAIL, naming the files still asking. By this point that includes at least:
  - `PinAssignmentScreen.tsx` and `PinAssignmentEditor.tsx`;
  - `retainedScreen.tsx`;
  - `connectionRecovery.ts`;
  - `ConnectionStatus.tsx`, if it still has importers;
  - the administration screens: `KeybindingPreferencesScreen.tsx`, `LaunchSettingsScreen.tsx`, `LaunchModelPicker.tsx`, `LaunchFallbackEditor.tsx`, `LaunchScalarEditor.tsx` and `LaunchResourceEditor.tsx`, and `HubSettingsScreen.tsx` if its wall still offers Reconnect;
  - any other phase's leftovers.
- [ ] **Step 3: Retire each one.**
  - **A Reconnect button:** delete it. The app reconnects on its own, and `SheetStatus` or the Board's toolbar says so.
  - **A "Reconnect to …" sentence:** delete it. Where a disabled control needs a reason, the connection line already gives it.
  - **Pin assignment:** drop `<Action onPress={retry}>Reconnect</Action>` and the "Reconnect to change pin assignments." reason. Render `SheetStatus` above the editor, and keep its controls disabled while `!connected`, as today.
  - **`HUB_NO_LONGER_SELECTED`:** it becomes "This hub is no longer selected."
  - **`connectionRecovery.ts`:**
    - the protocol message becomes `INCOMPATIBLE_VERSIONS`, which Task 2 declared in this same module;
    - the transport message becomes "Couldn't reach the hub. Check its address, its token and your network.".
  - **The administration screens** (ruling 12), which keep their own controls until #2539 but lose the ask:
    - `KeybindingPreferencesScreen.tsx`: its Reconnect button (`:172`) goes, with `SheetStatus` above the editor and its controls disabled while `!connected`, as pin assignment does; its "This hub is no longer selected. Return to Hubs to reconnect." (`:151`) becomes `HUB_NO_LONGER_SELECTED`.
    - `LaunchSettingsScreen.tsx`: its Reconnect button (`:140`) goes the same way.
    - `HubSettingsScreen.tsx`: its `ConnectionWall` and `ConnectionStatus` give way to `SheetStatus`, and the page keeps its last data while the connection is away.
    - The offline sentences keep their promise and drop the ask: `LaunchModelPicker.tsx:71` becomes "This hub's models show when it's back. Your selection is kept."; `LaunchFallbackEditor.tsx:168`, "This hub's models show when it's back. Your fallbacks are kept here."; `LaunchResourceEditor.tsx:213`, "Entries are checked when the hub is back. Your draft is kept here."
    - The errors a save raises offline say what happened: `LaunchScalarEditor.tsx:68` becomes "This path can't be checked until the hub is back.", and `LaunchResourceEditor.tsx:66`, "This entry can't be checked until the hub is back."
  - **`ConnectionStatus`, `ConnectionWall` and `ModalConnectionStatus`:** delete them once unimported.
  - **`useConnection().retry`:** keep it only if something still calls it (`grep -rn "\.retry\b\|retry()" mobile-native/src`). If nothing does, remove it from `ConnectionProvider` too, since the app retries on its own.
  - Update each touched screen's test to the new copy.
- [ ] **Step 4: Run it and watch it pass**, with the touched tests. Run: `cd mobile-native && npx vitest run src/noReconnect.test.ts src/PinAssignmentScreen.test.tsx src/connection.test.ts src/KeybindingPreferencesScreen.render.test.tsx src/HubSettingsScreen.reconnect.test.tsx && npm run check`. If a pin assignment test file has another name, run that file.
- [ ] **Step 5: Commit** (`fix(native): no screen asks you to reconnect`).

### Task 28: DESIGN.md describes the two sheets

Ruling 12 keeps today's administration screens on the phone, so this task deletes nothing: the Hub's MORE group and Providers' MANAGE groups stay.

**Files:**
- Modify: `mobile-native/DESIGN.md` and `mobile-native/.impeccable/design.json`.

**Requirements:**
1. `DESIGN.md` describes the two sheets:
   - the grouped list and its rules (glyphs in `inkMid`, amber only where a human is needed, the gray drift tag);
   - the New session's rows and pickers;
   - the Hub's pages, and its MORE group of today's administration screens, which keep their own look until #2539;
   - the calm connection line.

   `design.json` gains their components.

- [ ] Steps: edit both, then commit (`docs(native): DESIGN.md describes the New session and Hub sheets`).

### Task 29: Screenshots for the phase's last PR

Run the demo hub with `EVENER_DEMO_FLEET=1` and a Release simulator build (iPhone 17 Pro, 393×852). Start one session first, so the sheet has a remembered start to open on. Then capture Appendix A frames 20-24 in light and dark:

20. New session, filled:
    - the prompt;
    - Host magic-kingdom, Project evener, Branch main;
    - Model GLM 5.3 Vision via lunaroute, Effort XHigh;
    - Plugins "10 of 14", Access Workspace write, and More options.
21. The plugin checklist, grouped by marketplace, with superpowers-chrome's warning.
22. The model picker: Hub default, Recent, then the provider groups.
23. The Hub's home, taken with `EVENER_DEMO_FLEET_OFFLINE_HOST=1`:
    - "Hosts" with its "1 offline" tag;
    - "Providers" with "1 to sign in";
    - Plugins, Display, Hubs and About.
    - The spec's "plugins (one update)" can't show (ruling 7), and the PR says so.
24. paradise-park's detail, offline: its status, the "Hub runs 0.9.412" tag, its last error, Connect, and the offline footer.

Save them under `docs/design/mobile/assets/2026-09-2x-redesign-phase5-*.png`, named by frame and theme. Attach them to PR 13, with the demo hub's command in its description.

- [ ] Commit the screenshots (`docs(mobile): phase 5 screenshots`) and open PR 13: "feat(native): the last Reconnect goes, DESIGN.md, and screenshots (phase 5, PR 13)".

---

## Self-review against the spec

- **Spec 11, New session:**
  - the prompt first, with images: Task 20;
  - Host, with the offline state and Connect: Tasks 20 and 10 (ruling 4);
  - the host change keeping or moving the project: Tasks 16 and 19 (ruling 17);
  - Project, with recent projects, browsing and New folder: Task 21;
  - Model, with recent models, provider groups, capability glyphs, context and price: Task 21;
  - Effort, with only the model's levels: Task 20;
  - Plugins, with the grouped checklist, All and None, counts, warnings and the blocking problem: Tasks 22 and 20;
  - Access and network: Task 22 (ruling 14);
  - Branch: Task 22 (rulings 15 and 26);
  - More options: Task 22;
  - Start, replacing the sheet: Task 20. Rejections: Task 20 (ruling 20);
  - "New session like this": Task 25 (ruling 24).
- **Spec 12, the Hub:**
  - the header: Tasks 3 and 5;
  - bare glyphs: Task 1;
  - Hosts and host detail: Tasks 10 and 11 (rulings 3, 4 and 5);
  - Providers and sign-in: Tasks 12 to 14 (ruling 6);
  - Plugins: Task 15 (ruling 7);
  - Display: Tasks 6 and 7 (ruling 8);
  - In-app alerts: phase 6 (ruling 11);
  - Hubs: Tasks 8 and 9;
  - About: Tasks 4 and 5 (ruling 22);
  - More, today's administration screens, and Providers' MANAGE groups: Tasks 5, 13 and 27 (ruling 12).
- **Spec 14 and principle 2:**
  - the connection line: Task 2;
  - no Reconnect anywhere: Tasks 3 to 22 for this phase's screens, and Task 27's sweep and guard (ruling 21).
- **Spec 15, first run:** Tasks 8 and 9 (ruling 23).
- **The brief's facts:**
  - the hub retries a dropped host, so "Offline · reconnecting" shows and Connect only for an unattached host: Task 10 (ruling 4);
  - "Expires in 3d" waits for S11b: ruling 6;
  - `needsLogin` (#2483) and rejected refreshes (#2479): ruling 6;
  - the Board's interim hub menu replaced: Task 3;
  - the Reconnect buttons on New session, pin assignment, transcript preferences and plugins: Tasks 20, 27, 7 and 15.
