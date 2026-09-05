import { createStore } from "zustand/vanilla";
import type {
  HarnessDescriptor,
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
  projects: string[];
  harnesses: HarnessDescriptor[];
  models: ModelDescriptor[];
  loadingModels: boolean;
  submitting: boolean;
  error: string | null;
  catalogError: string | null;
  bind(service: NewSessionService | null): void;
  setCwd(value: string, refresh?: boolean): Promise<void>;
  setHarness(value: string): Promise<void>;
  setPrompt(value: string): void;
  selectModel(value: ModelDescriptor | null): void;
  setReasoning(value: string): void;
  loadMetadata(): Promise<void>;
  loadModels(): Promise<void>;
  submit(): Promise<Outcome>;
}
export function createNewSessionStore(hubId: string) {
  let service: NewSessionService | null = null;
  let connection = 0;
  let catalog = 0;
  return createStore<Form>((set, get) => ({
    cwd: "",
    prompt: "",
    harness: "",
    model: null,
    reasoning: "",
    projects: [],
    harnesses: [],
    models: [],
    loadingModels: false,
    submitting: false,
    error: null,
    catalogError: null,
    bind(next) {
      if (service === next) return;
      service = next;
      connection++;
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
      catalog++;
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
        if (generation === connection) set({ projects, harnesses });
      } catch {
        if (generation === connection)
          set({
            catalogError:
              "Could not load projects and harnesses. Retry options or use hub defaults.",
          });
      }
    },
    async loadModels() {
      const current = service;
      const generation = ++catalog;
      set({
        models: [],
        model: null,
        reasoning: "",
        loadingModels: !!current,
        catalogError: null,
      });
      if (!current) return;
      const { cwd, harness } = get();
      try {
        const result = await current.models({
          ...(cwd.trim() ? { cwd: cwd.trim() } : {}),
          ...(harness ? { harness } : {}),
        });
        if (generation === catalog) set({ models: result.data });
      } catch {
        if (generation === catalog)
          set({
            catalogError:
              "Could not load models. Retry options or use the hub default.",
          });
      } finally {
        if (generation === catalog) set({ loadingModels: false });
      }
    },
    async submit() {
      const current = service;
      const generation = connection;
      const { cwd, prompt, harness, model, reasoning, submitting } = get();
      if (!current || submitting || !cwd.trim()) return { status: "blocked" };
      set({ submitting: true, error: null });
      try {
        const result = await current.start({
          cwd: cwd.trim(),
          ...(prompt.trim() ? { input: [{ type: "text", text: prompt }] } : {}),
          ...(harness ? { harness } : {}),
          ...(model
            ? { model: model.model, modelProvider: model.provider }
            : {}),
          ...(model?.reasoningEffortLevels?.includes(reasoning)
            ? { reasoningEffort: reasoning }
            : {}),
        });
        if (generation !== connection) return { status: "obsolete" };
        return { status: "created", hubId, thread: result.thread };
      } catch {
        if (generation !== connection) return { status: "obsolete" };
        set({
          error:
            "Creation failed or could not be confirmed. Your input is kept. Check the session list before trying again; the session may exist.",
        });
        return { status: "failed" };
      } finally {
        if (generation === connection) set({ submitting: false });
      }
    },
  }));
}
