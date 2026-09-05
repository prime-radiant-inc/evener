# Native mobile first slice implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Run a basic shared native Evener client on iOS and Android.
**Architecture:** Expo app in mobile-native imports existing AppWire and mobile state code. A native socket factory supplies bearer headers. Native stack screens consume a connection context and existing stores.
**Tech Stack:** Expo SDK 57, React Native, TypeScript, React Navigation native stack, SecureStore, Zustand.
**Spec:** docs/superpowers/specs/2026-09-05-native-mobile-first-slice-design.md

## Global constraints

- Preserve old mobile implementation and local signing edits.
- No Tauri or DOM code in the native dependency graph.
- Token in native secure storage and socket header, never URL/log.
- No automatic replay of mutations; enforce capabilities and instance identity.
- Native safe areas, keyboard handling, platform back gestures, accessible labels.
- Voice and session creation are outside this slice.

## Task 1: Runnable native shell and connection

Files: mobile-native/package.json, app.json, metro.config.js, App.tsx,
src/connection.ts, src/connection.test.ts, src/ConnectionProvider.tsx.

- [ ] Generate a blank TypeScript Expo app; install native stack, safe areas,
  screens, SecureStore, Zustand, and the compatible SDK dependencies.
- [ ] Test origin normalization and rejection of embedded credentials/query,
  then implement `connectionTarget(origin: string): string` returning /rpc
  WebSocket URL. Test `connectClient` with a network-boundary socket and real
  AppwireClient handshake; assert header authentication and cleanup.
- [ ] Supply connection state and connect/disconnect through React context;
  save manually entered profile only in SecureStore. Close on background and
  reconnect on foreground, reloading current session after ready.
- [ ] Verify TypeScript and unit tests, commit.

## Task 2: Native session and conversation screens

Files: mobile-native/src/screens.tsx, src/TimelineItem.tsx, App.tsx.
Interfaces: context exposes AppwireClient|null and connection state;
createRosterService(client).list() supplies roster; createConversationService
and createConversationStore supply transcript and mutations.

- [ ] Render native stack with Connect, Sessions, Conversation routes.
- [ ] Use FlatList for roster, pull-to-refresh, loading/error/empty states.
- [ ] Open conversation via existing service/store; render text rows and
  expandable activity, native multiline composer, capability gated send/stop.
- [ ] On ready after reconnect rehydrate; unsubscribe/close on unmount.
- [ ] Exercise actual screens on both native simulators, including navigation,
  keyboard, scrolling, and connection error. Commit after typecheck.

## Task 3: Verification and review

- [ ] Test with deterministic network boundary or isolated scripted hub; do
  not send messages to existing real sessions as an incidental test.
- [ ] Run unit tests, typecheck, native bundles on both platforms.
- [ ] Capture simulator screenshots and independently review the change.
- [ ] Document exact launch commands and verified versus unverified behavior.
