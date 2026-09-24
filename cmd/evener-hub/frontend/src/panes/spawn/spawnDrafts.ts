// New-session drafts live only for this page's lifetime, keyed by the exact
// working directory used by launch/default resolution. No image bytes or
// unsent prompts are persisted. Each draft owns its async attachment pipeline.

import type {
  AdvancedValues,
  LaunchConfigLayer,
  PluginSelectionError,
  PluginSelectionState,
} from "@evener/appwire-client";
import { type Dispatch, type SetStateAction, useCallback } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { createAttachmentStore } from "../session/composer/attachments/useAttachments";
import { resolveInitialDefaults } from "./spawnDefaults";
import { readUrlPrefill } from "./urlPrefill";

interface DraftFields {
  prompt: string;
  promptRevision: number;
  harness: string;
  model: string;
  /** The model value the uncredentialed-default fallback installed into `model`
   * for THIS draft, or null when `model` did not come from it. Provenance is a
   * property of the draft, not of the mounted form: SpawnForm is a singleton
   * reused across drafts (no key) and is unmounted/remounted with the pane, so
   * a form-local marker leaked one draft's provenance onto an identical model
   * string in another draft and vanished on remount (Component 06b review,
   * round eight). */
  defaultModelFallback: string | null;
  staleModelNotice: string | null;
  reasoningEffort: string;
  accessMode: string;
  /** Launch source id for this draft (Component 06b's host picker). "local"
   * is the default and is omitted from the wire by startThread. */
  source: string;
  advancedOverrides: LaunchConfigLayer;
  advancedValues: AdvancedValues;
  pluginSelection: PluginSelectionState;
  knownSelectionIssues: PluginSelectionError[];
  /** Path-validation messages for advancedValues, keyed by wireField. Owned by
   * the draft alongside the `invalid` flag it explains: a validation that
   * settles while the draft is inactive (or a pane remount) must still be able
   * to show why the field is excluded from launchOverrides when it returns. */
  advancedErrors: Record<string, string>;
  busy: boolean;
  busyStartedAt: number | null;
  /** The pending "Create directory and start?" confirmation, and the host that
   * PREFLIGHTED the path it names (component 07b review, round seven). The two
   * travel together because the confirmation is an action against one host:
   * the dialog can outlive the selection that opened it (the manifest's
   * `online` flag is live), and a confirm that fires against a different host
   * is a different action than the one the user was offered. Kept in the draft
   * rather than component state for the same reason the path is: a pane
   * remount restores the dialog, so the binding has to survive it too. */
  createDialogPath: string | null;
  createDialogHost: string | null;
}

function createDraft(cwd: string) {
  const defaults = resolveInitialDefaults({ serverPrefillDir: cwd });
  return {
    cwd,
    fields: createStore<DraftFields>(() => ({
      prompt: "",
      promptRevision: 0,
      harness: defaults.harness ?? "",
      model: defaults.model ?? "",
      defaultModelFallback: null,
      staleModelNotice: null,
      reasoningEffort: defaults.reasoningEffort ?? "",
      accessMode: defaults.accessMode ?? "",
      source: "local",
      advancedOverrides: {},
      advancedValues: {},
      pluginSelection: { mode: "default" },
      knownSelectionIssues: [],
      advancedErrors: {},
      busy: false,
      busyStartedAt: null,
      createDialogPath: null,
      createDialogHost: null,
    })),
    attachments: createAttachmentStore(),
    busyRef: { current: false },
  };
}

export type SpawnDraft = ReturnType<typeof createDraft>;

export const spawnDraftsStore = createStore(() => ({
  drafts: new Map<string, SpawnDraft>(),
  current: null as SpawnDraft | null,
  lastPrefillURL: null as string | null,
  prefillRevision: 0,
}));

export function selectSpawnDirectory(cwd: string): SpawnDraft {
  const state = spawnDraftsStore.getState();
  let draft = state.drafts.get(cwd);
  const drafts = new Map(state.drafts);
  // Composing before choosing the first folder is supported: selecting it
  // assigns the unscoped draft rather than throwing away that initial work.
  if (!draft && cwd !== "" && state.current?.cwd === "") {
    draft = { ...state.current, cwd };
    drafts.delete("");
  }
  draft ??= createDraft(cwd);
  drafts.set(cwd, draft);
  spawnDraftsStore.setState({ drafts, current: draft });
  return draft;
}

export function applySpawnURL(onNavigation = false): void {
  const state = spawnDraftsStore.getState();
  const isNew = window.location.pathname === "/new";
  const applyPrefill = isNew && (onNavigation || state.lastPrefillURL !== window.location.href);
  const prefill = applyPrefill ? readUrlPrefill(window.location.search) : {};
  // A bare /new restores the last in-app selection even if no launch has ever
  // saved the global working-directory default.
  const urlDir = prefill.dir?.trim();
  const cwd = urlDir || (state.current?.cwd ?? resolveInitialDefaults({}).workingDir ?? "");
  const draft = selectSpawnDirectory(cwd);
  if (applyPrefill) {
    if (prefill.prompt) setDraftField(draft, "prompt", prefill.prompt);
    // The rail's project-copy launch hands its host over as ?host=; Spawn's
    // host picker owns the fallback when that host is unknown or offline.
    if (prefill.host) setDraftField(draft, "source", prefill.host);
    spawnDraftsStore.setState({ lastPrefillURL: window.location.href, prefillRevision: state.prefillRevision + 1 });
  }
}

// The same-URL suppression above is valid only while the route never leaves
// /new: returning to an explicit /new?dir=... URL is a fresh request that
// must re-apply its prefill. The pane cannot observe such a departure while
// it is unmounted (a single-pane host mounts only the active route), so the
// marker's invalidation lives at module scope, alongside the store it
// protects. A remount with no intervening navigation fires no popstate and
// keeps the marker, so its suppression of a redundant re-apply survives.
if (typeof window !== "undefined") {
  window.addEventListener("popstate", () => {
    if (window.location.pathname === "/new") return;
    const state = spawnDraftsStore.getState();
    if (state.lastPrefillURL !== null) spawnDraftsStore.setState({ lastPrefillURL: null });
  });
}

export function setDraftField<K extends keyof DraftFields>(
  draft: SpawnDraft,
  key: K,
  update: SetStateAction<DraftFields[K]>,
): void {
  const state = draft.fields.getState();
  const value =
    typeof update === "function" ? (update as (previous: DraftFields[K]) => DraftFields[K])(state[key]) : update;
  if (Object.is(state[key], value)) return;
  draft.fields.setState({
    [key]: value,
    ...(key === "prompt" ? { promptRevision: state.promptRevision + 1 } : {}),
  });
}

export function useDraftField<K extends keyof DraftFields>(
  draft: SpawnDraft,
  key: K,
): [DraftFields[K], Dispatch<SetStateAction<DraftFields[K]>>] {
  const value = useStore(draft.fields, (state) => state[key]);
  const setValue = useCallback((next: SetStateAction<DraftFields[K]>) => setDraftField(draft, key, next), [draft, key]);
  return [value, setValue];
}

export function resetSpawnDraftsForTests(): void {
  spawnDraftsStore.setState({ drafts: new Map(), current: null, lastPrefillURL: null, prefillRevision: 0 });
}
