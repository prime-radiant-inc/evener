// New-session drafts live only for this page's lifetime, keyed by the exact
// working directory used by launch/default resolution. No image bytes or
// unsent prompts are persisted. Each draft owns its async attachment pipeline.
import { type Dispatch, type SetStateAction, useCallback } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { LaunchConfigLayer } from "../../protocol/types.gen";
import { createAttachmentStore } from "../session/composer/attachments/useAttachments";
import type { PluginSelectionState } from "./pluginSelectionState";
import type { AdvancedValues } from "./schema";
import { resolveInitialDefaults } from "./spawnDefaults";
import { readUrlPrefill } from "./urlPrefill";

interface DraftFields {
  prompt: string;
  promptRevision: number;
  harness: string;
  model: string;
  reasoningEffort: string;
  accessMode: string;
  advancedOverrides: LaunchConfigLayer;
  advancedValues: AdvancedValues;
  pluginSelection: PluginSelectionState;
  busy: boolean;
  busyStartedAt: number | null;
  createDialogPath: string | null;
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
      reasoningEffort: defaults.reasoningEffort ?? "",
      accessMode: defaults.accessMode ?? "",
      advancedOverrides: {},
      advancedValues: {},
      pluginSelection: { mode: "default" },
      busy: false,
      busyStartedAt: null,
      createDialogPath: null,
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
  const cwd = prefill.dir ?? state.current?.cwd ?? resolveInitialDefaults({}).workingDir ?? "";
  const draft = selectSpawnDirectory(cwd);
  if (applyPrefill) {
    if (prefill.prompt) setDraftField(draft, "prompt", prefill.prompt);
    spawnDraftsStore.setState({ lastPrefillURL: window.location.href, prefillRevision: state.prefillRevision + 1 });
  }
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
