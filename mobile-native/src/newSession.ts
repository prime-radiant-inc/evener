import { createStore } from "zustand/vanilla";
import { MAX_ATTACHMENTS } from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/limits";
import {
  markerText,
  stripMarker,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/textareaMarkers";
import { harnessSupportsPluginSelection } from "../../cmd/evener-hub/frontend/src/panes/spawn/harnessModels";
import {
  pluginSelectionIssues,
  withPluginSelection,
} from "../../cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState";
import { resolveScalars } from "../../cmd/evener-hub/frontend/src/panes/spawn/schema";
import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  HarnessDescriptor,
  LaunchConfigLayer,
  ModelDescriptor,
  Thread,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { buildComposerInput } from "../../cmd/evener-hub/frontend/src/stores/composerInput";
import type { NewSessionService } from "../../mobile/src/services/newSession";
import { type DraftImageData, imageInput } from "./draftImages";

type Outcome =
  | { status: "created"; hubId: string; thread: Thread }
  | { status: "blocked" | "failed" | "obsolete" };
interface Form {
  cwd: string;
  prompt: string;
  images: DraftImageData[];
  addImage(image: DraftImageData): void;
  removeImage(id: string): void;
  harness: string;
  model: ModelDescriptor | null;
  reasoning: string;
  launchOverrides: LaunchConfigLayer;
  setLaunchOverrides(value: LaunchConfigLayer): void;
  projects: string[];
  harnesses: HarnessDescriptor[];
  models: ModelDescriptor[];
  loadingModels: boolean;
  submitting: boolean;
  error: string | null;
  metadataError: string | null;
  modelError: string | null;
  bind(service: NewSessionService | null): void;
  setCwd(value: string, refresh?: boolean): Promise<void>;
  setHarness(value: string): Promise<void>;
  setPrompt(value: string): void;
  selectModel(value: ModelDescriptor | null): void;
  setReasoning(value: string): void;
  loadMetadata(): Promise<void>;
  loadModels(refresh?: boolean): Promise<void>;
  submit(): Promise<Outcome>;
}
export function creationModel(
  models: ModelDescriptor[],
  selected: ModelDescriptor | null,
  overrides: LaunchConfigLayer,
): ModelDescriptor | null {
  const id = overrides.model?.trim();
  if (!id) return selected;
  const matches = models.filter(
    (model) => `${model.provider}/${model.model}` === id || model.model === id,
  );
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

export function createNewSessionStore(hubId: string) {
  let service: NewSessionService | null = null;
  let connection = 0;
  let catalog = 0;
  let refreshingModels = false;
  let loadedContext: string | null = null;
  let creationRequested = false;
  return createStore<Form>((set, get) => ({
    cwd: "",
    prompt: "",
    images: [],
    addImage(image) {
      const state = get();
      if (state.submitting) return;
      if (
        state.images.length >= MAX_ATTACHMENTS ||
        state.images.some(
          (item) => item.id === image.id || item.marker === image.marker,
        )
      )
        throw new Error("This image cannot be added to the draft.");
      set({
        images: [...state.images, { ...image }],
        prompt: state.prompt + markerText(image.marker),
      });
    },
    removeImage(id) {
      const state = get();
      if (state.submitting) return;
      const image = state.images.find((item) => item.id === id);
      if (!image) return;
      set({
        images: state.images.filter((item) => item.id !== id),
        prompt: stripMarker(state.prompt, undefined, image.marker).value,
      });
    },
    harness: "",
    model: null,
    reasoning: "",
    launchOverrides: {},
    setLaunchOverrides(value) {
      if (!get().submitting)
        set({ launchOverrides: JSON.parse(JSON.stringify(value)) });
    },
    projects: [],
    harnesses: [],
    models: [],
    loadingModels: false,
    submitting: false,
    error: null,
    metadataError: null,
    modelError: null,
    bind(next) {
      if (service === next) return;
      const uncertainCreation = get().submitting && creationRequested;
      creationRequested = false;
      service = next;
      connection++;
      refreshingModels = false;
      loadedContext = null;
      catalog++;
      set({
        projects: [],
        harnesses: [],
        models: [],
        model: null,
        reasoning: "",
        loadingModels: false,
        submitting: false,
        ...(uncertainCreation
          ? {
              error:
                "Creation could not be confirmed. Check the session list before trying again; the session may exist.",
            }
          : {}),
      });
    },
    async setCwd(cwd, refresh = true) {
      if (cwd.trim() === get().cwd.trim()) {
        set({ cwd });
        if (refresh) await get().loadModels();
        return;
      }
      loadedContext = null;
      catalog++;
      refreshingModels = false;
      set({
        cwd,
        models: [],
        model: null,
        reasoning: "",
        loadingModels: false,
      });
      if (refresh) await get().loadModels();
    },
    async setHarness(harness) {
      if (get().submitting) return;
      set({
        harness,
        launchOverrides: harnessSupportsPluginSelection(
          harness,
          get().harnesses,
        )
          ? get().launchOverrides
          : withPluginSelection(get().launchOverrides, { mode: "default" }),
      });
      await get().loadModels();
    },
    setPrompt(prompt) {
      set({ prompt });
    },
    selectModel(value) {
      const model =
        get().models.find(
          (m) => m.provider === value?.provider && m.model === value.model,
        ) ?? null;
      const launchOverrides = { ...get().launchOverrides };
      const reasoning = launchOverrides.reasoningEffort || get().reasoning;
      delete launchOverrides.model;
      delete launchOverrides.reasoningEffort;
      set({
        model,
        launchOverrides,
        reasoning: model?.reasoningEffortLevels?.includes(reasoning)
          ? reasoning
          : "",
      });
    },
    setReasoning(value) {
      const state = get();
      const model = creationModel(
        state.models,
        state.model,
        state.launchOverrides,
      );
      const launchOverrides = { ...state.launchOverrides };
      delete launchOverrides.reasoningEffort;
      set({
        launchOverrides,
        reasoning: model?.reasoningEffortLevels?.includes(value) ? value : "",
      });
    },
    async loadMetadata() {
      const current = service;
      const generation = connection;
      if (!current) return;
      try {
        const [projects, harnesses] = await Promise.all([
          current.recentProjects(),
          current.harnesses(),
        ]);
        if (generation === connection)
          set({ projects, harnesses, metadataError: null });
      } catch {
        if (generation === connection)
          set({
            metadataError:
              "Could not load projects and harnesses. Retry options or use hub defaults.",
          });
      }
    },
    async loadModels(refresh = false) {
      const current = service;
      const { cwd, harness } = get();
      const context = JSON.stringify([cwd.trim(), harness]);
      if (current && loadedContext === context && !refresh) return;
      const selection = loadedContext === context ? get().model : null;
      const reasoning = get().reasoning;
      loadedContext = null;
      const generation = ++catalog;
      refreshingModels = !!current && refresh;
      set({
        models: [],
        model: null,
        reasoning: "",
        loadingModels: !!current,
      });
      if (!current) return;
      try {
        const result = await current.models({
          ...(cwd.trim() ? { cwd: cwd.trim() } : {}),
          ...(harness ? { harness } : {}),
        });
        if (generation === catalog) {
          loadedContext = context;
          const model =
            result.data.find(
              (item) =>
                item.provider === selection?.provider &&
                item.model === selection.model,
            ) ?? null;
          const settingsModel = creationModel(
            result.data,
            model,
            get().launchOverrides,
          );
          set({
            models: result.data,
            model,
            reasoning: settingsModel?.reasoningEffortLevels?.includes(reasoning)
              ? reasoning
              : "",
            modelError: null,
          });
        }
      } catch {
        if (generation === catalog)
          set({
            modelError:
              "Could not load models. Retry options or use the hub default.",
          });
      } finally {
        if (generation === catalog) {
          refreshingModels = false;
          set({ loadingModels: false });
        }
      }
    },
    async submit() {
      const current = service;
      const generation = connection;
      const { cwd, prompt, harness, model, reasoning, submitting } = get();
      if (!current || submitting || refreshingModels || !cwd.trim())
        return { status: "blocked" };
      const launchOverrides = harnessSupportsPluginSelection(
        harness,
        get().harnesses,
      )
        ? get().launchOverrides
        : withPluginSelection(get().launchOverrides, { mode: "default" });
      const settingsModel = creationModel(get().models, model, launchOverrides);
      const input = buildComposerInput(
        prompt,
        get().images.map((image) => imageInput(image, image.data)),
      );
      const scalars = resolveScalars(
        {
          model: model?.model,
          modelProvider: model?.provider,
          reasoningEffort: settingsModel?.reasoningEffortLevels?.includes(
            reasoning,
          )
            ? reasoning
            : undefined,
        },
        launchOverrides,
      );
      set({ submitting: true, error: null });
      let startDispatched = false;
      creationRequested = false;
      try {
        if (launchOverrides.enabledPlugins !== undefined) {
          const preview = await current.previewPlugins({
            cwd: cwd.trim(),
            launchOverrides,
          });
          if (generation !== connection) return { status: "obsolete" };
          const issues = pluginSelectionIssues(
            { mode: "explicit", names: launchOverrides.enabledPlugins },
            preview,
          );
          if (issues.length) {
            set({
              error: issues
                .map((issue) => `${issue.name}: ${issue.reason}`)
                .join("\n"),
            });
            return { status: "blocked" };
          }
        }
        startDispatched = true;
        creationRequested = true;
        const result = await current.start({
          cwd: cwd.trim(),
          ...(input.length ? { input } : {}),
          ...(harness ? { harness } : {}),
          ...(scalars.model ? { model: scalars.model } : {}),
          ...(scalars.modelProvider
            ? { modelProvider: scalars.modelProvider }
            : {}),
          ...(scalars.reasoningEffort
            ? { reasoningEffort: scalars.reasoningEffort }
            : {}),
          ...(Object.keys(launchOverrides).length ? { launchOverrides } : {}),
        });
        if (generation !== connection) return { status: "obsolete" };
        return { status: "created", hubId, thread: result.thread };
      } catch (error) {
        if (generation !== connection) return { status: "obsolete" };
        set({
          error: startDispatched
            ? (error instanceof WireError ? `${error.message}\n\n` : "") +
              "Creation failed or could not be confirmed. Your input is kept. Check the session list before trying again; the session may exist."
            : "Could not validate selected plugins. No session was requested. Reconnect or retry; your selection is kept.",
        });
        return { status: "failed" };
      } finally {
        if (generation === connection) set({ submitting: false });
      }
    },
  }));
}
