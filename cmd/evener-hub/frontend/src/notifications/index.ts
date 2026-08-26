// The notifications engine: owns document.title's attention count, the
// favicon badge, OS notifications, the alert sound, and single-tab (Web
// Locks) leader election, all driven off treeStore + prefsStore +
// workspaceStore + connectionStore. AppShell calls initNotifications() once
// at module evaluation, beside initPrefs().
//
// Every channel is read EXCLUSIVELY from the shipped prefs store (title ON,
// favicon/os/sound OFF by default — stores/prefs.ts's loadNotifications and
// docs/web-ui/decisions.md's 2026-08-14 entry) — this engine introduces NO
// default layer of its own. The legacy runtime's favicon-TRUE default stays
// deliberately unported. See title.ts / favicon.ts / channels.ts for each
// channel; attention.ts for the pure transition detection; leader.ts for the
// Web-Locks-only election.

import { workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { prefsStore } from "../stores/prefs";
import { treeStore } from "../stores/tree";
import { type AttentionEntry, detectFires, snapshotFromTree } from "./attention";
import { fireOsNotification, playTone } from "./channels";
import { applyFavicon } from "./favicon";
import { electLeader, isLeader } from "./leader";
import { applyTitle } from "./title";

let initialized = false;
// null until the first treeStore snapshot establishes the baseline. Edge-fire
// diffs each later snapshot against this; a page load therefore never
// re-alerts on attention that was already present (floor §3.6).
let prevSnapshot: Map<string, AttentionEntry> | null = null;
// Set on reconnect so the next snapshot re-baselines silently (the gap may
// have missed broadcasts; re-sync rather than replay them as fresh alerts).
let rebaselinePending = false;
// Whether we have already seen the connection reach "ready" once — so the
// initial connect is distinguished from a reconnect.
let sawReady = false;
let prevNavigationAttention: Map<string, AttentionEntry> | null = null;
const subscriptions: Array<() => void> = [];

function currentSummary() {
  return navigationStore.getState().mode === "v1"
    ? navigationStore.getState().attention.summary
    : (treeStore.getState().tree?.attentionSummary ?? null);
}

function onNavigationAttention(): void {
  const state = navigationStore.getState();
  applyCounts();
  if (state.mode !== "v1") return;
  if (state.attention.changed.length === 0 && prevNavigationAttention === null) return;
  const next = new Map(prevNavigationAttention);
  for (const changed of state.attention.changed) {
    const level = changed.level === "error" ? "error" : changed.level === "needs_you" ? "needs_you" : null;
    if (!level) {
      next.delete(changed.threadId);
      continue;
    }
    next.set(changed.threadId, {
      ref: changed.threadId,
      title: changed.title,
      level,
      askPending: changed.askPending === true,
    });
  }
  if (prevNavigationAttention === null) {
    prevNavigationAttention = next;
    return;
  }
  const { notifications, notificationsLoudScope } = prefsStore.getState();
  const alerts = detectFires(prevNavigationAttention, next, notificationsLoudScope);
  prevNavigationAttention = next;
  if (alerts.length === 0 || document.hasFocus?.() || !isLeader()) return;
  for (const entry of alerts) {
    if (notifications.os) fireOsNotification(entry);
    if (notifications.sound) playTone();
  }
}

// Title base tracks the focused pane; both channels read their pref straight
// from the store (never defaulted on here).
function applyTitleNow(): void {
  applyTitle(prefsStore.getState().notifications.title, currentSummary());
}

function applyCounts(): void {
  const { notifications } = prefsStore.getState();
  applyTitle(notifications.title, currentSummary());
  applyFavicon(notifications.favicon, currentSummary());
}

function onTreeChanged(): void {
  if (navigationStore.getState().mode === "v1") return;
  // Counts (title + favicon) apply unconditionally on every snapshot — even
  // before a baseline, even focused, even on a non-leader tab (floor §3.6);
  // only the OS/sound edge-fire below is gated.
  applyCounts();

  const next = snapshotFromTree(treeStore.getState().tree);

  // The first snapshot IS the baseline; a reconnect re-baselines the same
  // way. Neither fires.
  if (prevSnapshot === null || rebaselinePending) {
    prevSnapshot = next;
    rebaselinePending = false;
    return;
  }

  const { notifications, notificationsLoudScope } = prefsStore.getState();
  const alerts = detectFires(prevSnapshot, next, notificationsLoudScope);
  prevSnapshot = next;

  // Edge-fire gates: only when unfocused AND this tab is the elected leader.
  // os and sound are then checked independently — either, both, or neither.
  if (alerts.length === 0) return;
  if (document.hasFocus?.()) return;
  if (!isLeader()) return;
  for (const entry of alerts) {
    if (notifications.os) fireOsNotification(entry);
    if (notifications.sound) playTone();
  }
}

export function initNotifications(): void {
  if (initialized) return;
  initialized = true;

  electLeader();

  // Navigation is authoritative when the handshake advertises it. The
  // microtask lets AppShell wire its client during the same mount before the
  // migration-only tree fallback is considered.
  queueMicrotask(() => {
    const { client, state } = connectionStore.getState();
    if (client && state === "ready") initNavigation(client);
  });

  subscriptions.push(
    connectionStore.subscribe((state, prev) => {
      if (state.client && state.state === "ready" && (state.client !== prev.client || prev.state !== "ready"))
        initNavigation(state.client);
    }),
  );
  subscriptions.push(
    navigationStore.subscribe((state, prev) => {
      if (state.attention !== prev.attention) onNavigationAttention();
      if (state.resources !== prev.resources) applyTitleNow();
      if (prev.mode === "v1" && state.mode !== "v1") prevNavigationAttention = null;
      if (state.mode === "legacy" && prev.mode !== "legacy" && treeStore.getState().tree === null)
        void treeStore.getState().ensureLoaded();
    }),
  );

  // React to a tree snapshot only when the tree reference actually changes —
  // never on a bare loading/error transition, whose null-tree snapshot would
  // otherwise corrupt the baseline into "everything just appeared."
  subscriptions.push(
    treeStore.subscribe((state, prev) => {
      if (state.tree !== prev.tree) onTreeChanged();
    }),
  );

  // A notification-pref toggle re-applies title/favicon immediately (no edge
  // fire — this is not a tree transition).
  subscriptions.push(
    prefsStore.subscribe((state, prev) => {
      if (state.notifications !== prev.notifications) applyCounts();
    }),
  );

  // The base title tracks the focused pane.
  subscriptions.push(workspaceStore.subscribe(applyTitleNow));

  // Reconnect: on reaching "ready" AFTER a prior "ready", re-fetch the tree so
  // counts re-sync and the next snapshot re-baselines silently.
  //
  // Only a TRANSITION into "ready" is a connection event. Any other change to
  // this store republishes the same "ready" to every subscriber - AppShell
  // sets serverInfo through it the moment its own connect() promise resolves,
  // on every single boot - and reading that as a reconnect cost a pointless
  // extra GET /api/tree and a needless re-baseline of the attention snapshot
  // (kata p5w9).
  sawReady = connectionStore.getState().state === "ready";
  subscriptions.push(
    connectionStore.subscribe((state, prev) => {
      if (state.state !== "ready" || prev.state === "ready") return;
      if (!sawReady) {
        sawReady = true;
        return;
      }
      rebaselinePending = true;
      const client = connectionStore.getState().client;
      if (!client || navigationStore.getState().mode === "legacy") void treeStore.getState().refresh();
    }),
  );

  // Seed the baseline from any already-loaded tree so the first post-init
  // transition (not the first snapshot) is what fires.
  const current = treeStore.getState().tree;
  if (current !== null) prevSnapshot = snapshotFromTree(current);
  applyCounts();
}

// resetNotificationsForTests unwinds every store subscription and resets the
// module-private baseline/leader/init state. No production code should call
// it (mirrors the resetXStoreForTests precedent across stores/*).
export function resetNotificationsForTests(): void {
  for (const unsub of subscriptions) unsub();
  subscriptions.length = 0;
  initialized = false;
  prevSnapshot = null;
  rebaselinePending = false;
  sawReady = false;
  prevNavigationAttention = null;
  resetNavigationStoreForTests();
}
