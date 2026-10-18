# Share to Evener Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let iOS share text or a URL into Evener, pick a new or existing session, and land the content in that session's composer draft.

**Architecture:** A plain Swift share extension saves the shared payload to an App Group container and opens `evener-native://share` through the responder chain. The app scans that container on launch, foreground, hub ready, and URL receipt, then presents one `ShareTarget` sheet that reuses the Board's live rows and `evener/search`. Choosing a destination writes the shared text into that destination's durable draft before deleting the shared file.

**Tech Stack:** Expo SDK 57 / React Native 0.86 / React Navigation 7 / expo-file-system / expo-sqlite / @bacons/apple-targets / Swift UIKit.

**Spec:** `docs/superpowers/specs/2026-10-18-share-intent-design.md`

## Global Constraints

- Generated `mobile-native/ios/` stays gitignored; native config comes from `app.config.js` and `mobile-native/targets/`.
- The picker uses the app's active hub only.
- No test asserts on human-readable copy. Assert behavior.
- Extension shares text and URLs only. No images, files, videos.
- The shared payload is never logged.
- `app.json` becomes `app.config.js`; `ios.appleTeamId` comes from `APPLE_TEAM_ID`.
- Biome scope: `mobile-native/src` and `mobile-native/targets` JS config files; run from `mobile-native`.
- Gates: `make test-native` and `make test-native-bundle`.

## Review Focus

1. A share file that is malformed or half-written must be skipped and left in place, not crash intake or be silently deleted.
2. A failed draft write must leave both the sheet and the share file, so the user can retry.
3. Selecting a past session that cannot compose must still keep the text in its durable draft.
4. Dismissing the sheet without choosing must not delete the share; only explicit Cancel does.
5. The extension must not dismiss when the save fails; the user must see that the text was kept.

---

### Task 1: Share payload intake library

**Files:**
- Create: `mobile-native/src/share/sharePayload.ts`
- Create: `mobile-native/src/share/sharePayload.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export interface SharedPayload { id: string; text: string; createdAt: string }
  export interface SharePort {
    list(): Promise<string[]>;
    read(name: string): Promise<string>;
    remove(name: string): Promise<void>;
    now(): number;
  }
  export function parseSharePayload(fileName: string, raw: string): SharedPayload | null
  export function shareFileName(id: string): string
  export async function pendingShares(port: SharePort): Promise<SharedPayload[]>
  export async function completeShare(port: SharePort, id: string): Promise<void>
  export const SHARE_MAX_AGE_MS: number
  ```

- [ ] **Step 1: Write the failing test**

Assertions: `parseSharePayload` returns null for invalid JSON, missing id, non-string text, an id whose name does not match the file; returns the payload for a valid file; `pendingShares` skips malformed files, sorts oldest first, and returns an empty list for an empty directory; `completeShare` removes exactly `share-<id>.json`.

- [ ] **Step 2: Run and verify failure**

Run: `cd mobile-native && npx vitest run src/share/sharePayload.test.ts`
Expected: FAIL, module not found.

- [ ] **Step 3: Implement**

`shareFileName(id)` returns `share-${id}.json`. `parseSharePayload` requires `id` to be a non-empty string, `text` a non-empty trimmed string, `createdAt` an ISO string, and `fileName === shareFileName(id)`. `pendingShares` reads every listed name, drops nulls, sorts by `createdAt` ascending, and leaves unparsable files untouched. `completeShare` removes only `shareFileName(id)`. `SHARE_MAX_AGE_MS` is 7 days.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/share/sharePayload.ts mobile-native/src/share/sharePayload.test.ts
git commit -m "feat(native): read shared payloads from the app group"
```

### Task 2: App Group port over expo-file-system

**Files:**
- Create: `mobile-native/src/share/appGroupSharePort.ts`
- Create: `mobile-native/src/share/appGroupSharePort.test.ts`

**Interfaces:**
- Consumes: `SharePort` from Task 1.
- Produces: `export const SHARE_APP_GROUP = "group.com.primeradiant.evener.native"`, `export function appGroupSharePort(): SharePort`.

- [ ] **Step 1: Write the failing test**

A fake directory object records calls; assert `list` filters to `share-*.json`, `read` joins the directory and name, `remove` deletes only the named file, and a missing container yields an empty list rather than throwing.

- [ ] **Step 2: Run and verify failure** — module not found.

- [ ] **Step 3: Implement** over `Paths.appleSharedContainers[SHARE_APP_GROUP]`; absent container returns `[]` from `list`. No payload logging.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

### Task 3: Share intake hook

**Files:**
- Create: `mobile-native/src/share/useShareIntake.ts`
- Create: `mobile-native/src/share/useShareIntake.test.tsx`
- Modify: `mobile-native/App.tsx`

**Interfaces:**
- Consumes: `pendingShares`, `appGroupSharePort`, `useConnection()`.
- Produces: `export function useShareIntake(options: { onShare(item: SharedPayload): void; port?: SharePort }): void`.

- [ ] **Step 1: Write the failing test**

With a fake port and a fake Linking/AppState, assert: an initial `evener-native://share` URL opens the first pending item; a foreground rescan opens it; two shares open oldest first; the hook never opens the same id twice while its sheet is up; a non-share URL does nothing.

- [ ] **Step 2: Run and verify failure.**

- [ ] **Step 3: Implement** with guards: only one open item at a time, a claimed-id set, and a re-scan when `connectionState` turns ready.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

### Task 4: ShareTarget sheet

**Files:**
- Create: `mobile-native/src/share/ShareTargetSheet.tsx`
- Create: `mobile-native/src/share/ShareTargetSheet.test.tsx`
- Create: `mobile-native/src/share/shareSheetRoutes.ts`
- Modify: `mobile-native/App.tsx`, `mobile-native/src/screens.tsx` (`Routes`)

**Interfaces:**
- Consumes: `createBoardController`, `liveBands`, `createSearchController`, `sessionResults`, `drafts.open`, `completeShare`.
- Produces: route `ShareTarget: { item: SharedPayload }`; `export function ShareTargetSheet(props)`.

- [ ] **Step 1: Write the failing test** (fake client in the Board's test shape)

Assertions: live rows render in Needs you → Working → Idle order; typing a query sends one debounced `evener/search` and replaces the list; a past result is selectable; New session navigates with the share; choosing a session calls `drafts.open(...).edit(...)` with the shared text appended to an existing draft; a draft write error keeps the sheet open and the share pending; Cancel asks before discarding; "Connect to a hub" shows with no ready client.

- [ ] **Step 2: Run and verify failure.**

- [ ] **Step 3: Implement** the sheet with `useSheetStackOptions` styling, a preview, the New session row, the search field, and the sections.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

### Task 5: New session seeding

**Files:**
- Modify: `mobile-native/src/screens.tsx` (`NewSession` route params), `mobile-native/src/newSession/NewSessionSheet.tsx`
- Create: `mobile-native/src/newSession/shareSeed.ts`
- Create: `mobile-native/src/newSession/shareSeed.test.ts`

**Interfaces:**
- Produces: `export function seedSharedPrompt(store: NewSessionStore, share: { id: string; text: string }): boolean`; `Routes.NewSession` gains `share?: { id: string; text: string }`.

- [ ] **Step 1: Write the failing test** using the real store over a test draft storage: the remembered setup applies, the saved prompt survives, the shared text appends, the draft is saved, and a storage error returns false.

- [ ] **Step 2: Run and verify failure.**

- [ ] **Step 3: Implement** `seedSharedPrompt`: call `openForm(store, lastSetup)` first, then `setPrompt(mergeDraftText(prompt, text))`, and return `!store.getState().storageError`. `NewSessionSheet` calls it after `storageLoaded`, then `completeShare` on true, and shows the error on false.

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

### Task 6: iOS share extension target

**Files:**
- Create: `mobile-native/targets/share/expo-target.config.js`
- Create: `mobile-native/targets/share/Info.plist`
- Create: `mobile-native/targets/share/ShareViewController.swift`
- Create: `mobile-native/app.config.js` (replaces `mobile-native/app.json`)
- Delete: `mobile-native/app.json`
- Modify: `mobile-native/package.json`
- Modify: `mobile-native/scripts/configure-ios-distribution.rb`
- Modify: `mobile-native/scripts/test-ios-distribution.rb`
- Modify: `docs/design/mobile/ios-build-distribution.md`

**Interfaces:**
- Produces: target `EvenerShareExtension`, bundle id `com.primeradiant.evener.native.ShareExtension`, App Group `group.com.primeradiant.evener.native`, activation rules for text and one web URL.

- [ ] **Step 1: Install `@bacons/apple-targets`** at a version compatible with Expo 57 and record the exact version in `package.json`.

- [ ] **Step 2: Write the extension config and Swift view controller**: extract `public.plain-text` and `public.url`, join them with the URL last, write `share-<uuid>.json` to the App Group, prune beyond 10 files, open `evener-native://share` through the responder chain, and `completeRequest`. On failure show a kept-text message and a Done button.

- [ ] **Step 3: Prebuild and confirm the target appears**

Run: `cd mobile-native && npx expo prebuild --platform ios --no-install`
Expected: `ios/Evener.xcodeproj` contains `EvenerShareExtension` with the app group entitlement on both targets. Record the generated paths.

- [ ] **Step 4: Extend distribution signing** for the second bundle id and add `IOS_SHARE_PROVISIONING_PROFILE_NAME`, with tests in `test-ios-distribution.rb`.

- [ ] **Step 5: Commit**

### Task 7: Docs and verification

**Files:**
- Modify: `mobile-native/DESIGN.md`, `mobile-native/README.md`, `docs/product/subsystems.md` (only if the row names native surfaces)

- [ ] **Step 1:** Document the sheet, its rows, and its states in `DESIGN.md`.
- [ ] **Step 2:** Document the extension, App Group, team id, and the manual share checklist in `README.md` and `ios-build-distribution.md`.
- [ ] **Step 3:** Run `cd mobile-native && npm run check`, `npm run lint`, and `make test-native-bundle`.
- [ ] **Step 4:** Commit.
