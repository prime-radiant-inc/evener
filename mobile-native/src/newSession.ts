import { createStore } from "zustand/vanilla";
import { resolveScalars } from "../../cmd/evener-hub/frontend/src/panes/spawn/schema";
import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  HarnessDescriptor,
  LaunchConfigLayer,
  ModelDescriptor,
  Thread,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { NewSessionService } from "../../mobile/src/services/newSession";

type Outcome =
  | { status: "created"; hubId: string; thread: Thread }
  | { status: "blocked" | "failed" | "obsolete" };
interface Form {
  cwd: string;
  prompt: string;
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
export function createNewSessionStore(hubId: string) {
  let service: NewSessionService | null = null;
  let connection = 0;
  let catalog = 0;
  let refreshingModels = false;
  let loadedContext: string | null = null;
  return createStore<Form>((set, get) => ({
    cwd: "",
    prompt: "",
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
        ...(get().submitting
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
      set({ harness });
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
      set({
        model,
        reasoning: model?.reasoningEffortLevels?.includes(get().reasoning)
          ? get().reasoning
          : "",
      });
    },
    setReasoning(value) {
      set({
        reasoning: get().model?.reasoningEffortLevels?.includes(value)
          ? value
          : "",
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
          set({
            models: result.data,
            model,
            reasoning: model?.reasoningEffortLevels?.includes(reasoning)
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
      const launchOverrides = get().launchOverrides;
      const scalars = resolveScalars(
        {
          model: model?.model,
          modelProvider: model?.provider,
          reasoningEffort: model?.reasoningEffortLevels?.includes(reasoning)
            ? reasoning
            : undefined,
        },
        launchOverrides,
      );
      set({ submitting: true, error: null });
      try {
        const result = await current.start({
          cwd: cwd.trim(),
          ...(prompt.trim() ? { input: [{ type: "text", text: prompt }] } : {}),
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
          error:
            (error instanceof WireError ? `${error.message}\n\n` : "") +
            "Creation failed or could not be confirmed. Your input is kept. Check the session list before trying again; the session may exist.",
        });
        return { status: "failed" };
      } finally {
        if (generation === connection) set({ submitting: false });
      }
    },
  }));
}
