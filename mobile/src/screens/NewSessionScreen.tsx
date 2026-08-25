/**
 * New session screen — live form wired to the Hub via NewSessionService.
 *
 * Form fields: project path (with autocomplete from recent projects), initial
 * prompt, and optional model/effort. The Start button calls
 * newSessionService.start() and on success navigates to the new conversation
 * via navigation.pushConversation({ sessionId: thread.id, title: thread.preview }).
 * Inline errors from the server are shown below the form. The Start button is
 * disabled when the project path is empty.
 */
import { type JSX, useEffect, useState } from "react";
import type {
  ModelDescriptor,
  Thread,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  NewSessionParams,
  NewSessionService,
} from "../services/newSession";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { TopBar } from "../ui/TopBar";
import type { NavigationStore } from "./root-types";

export interface NewSessionScreenProps {
  readonly service?: NewSessionService;
  readonly navigation?: NavigationStore;
}

export function NewSessionScreen({
  service,
  navigation,
}: NewSessionScreenProps = {}): JSX.Element {
  const [projectPath, setProjectPath] = useState("");
  const [prompt, setPrompt] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [recentProjects, setRecentProjects] = useState<string[]>([]);
  const [models, setModels] = useState<ModelDescriptor[]>([]);
  const [selectedModel, setSelectedModel] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  // Load recent projects on mount for autocomplete suggestions.
  useEffect(() => {
    if (service === undefined) return;
    void service
      .recentProjects()
      .then((projects) => setRecentProjects(projects))
      .catch(() => {
        // Recent projects are best-effort; don't block the form.
      });
  }, [service]);

  // Model discovery is scoped by the server-side model/list protocol. It is
  // best-effort like recent projects: the default server model remains a valid
  // choice when a hub cannot enumerate models.
  useEffect(() => {
    if (service === undefined) return;
    let cancelled = false;
    setModelsLoading(true);
    setModelsError(null);
    void service
      .models()
      .then((response) => {
        if (cancelled) return;
        const discovered = response.data ?? [];
        const recent = response.recent ?? [];
        const seen = new Set<string>();
        const ordered = [...recent, ...discovered].filter((descriptor) => {
          const key = modelKey(descriptor);
          if (seen.has(key)) return false;
          seen.add(key);
          return true;
        });
        setModels(ordered);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setModelsError(err instanceof Error ? err.message : String(err));
        }
      })
      .finally(() => {
        if (!cancelled) setModelsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [service]);

  const canStart =
    projectPath.trim() !== "" && !starting && service !== undefined;

  async function handleStart(): Promise<void> {
    if (projectPath.trim() === "") return;
    if (service === undefined || navigation === undefined) return;
    setStarting(true);
    setError(null);
    try {
      const params: NewSessionParams = { cwd: projectPath.trim() };
      if (prompt.trim() !== "") {
        params.input = [{ type: "text", text: prompt.trim() }];
      }
      if (selectedModel !== "") {
        const separator = selectedModel.indexOf("\u0000");
        if (separator > 0) {
          params.modelProvider = selectedModel.slice(0, separator);
          params.model = selectedModel.slice(separator + 1);
        }
      }
      const result: { thread: Thread; turn: Turn } =
        await service.start(params);
      const title = result.thread.name ?? result.thread.preview ?? "";
      navigation.getState().pushConversation({
        sessionId: result.thread.id,
        title,
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setStarting(false);
    }
  }

  return (
    <div>
      <TopBar title="New Session" />
      <div className="evener-list-group">
        <Input
          label="Project path"
          name="project"
          placeholder="/path/to/project"
          value={projectPath}
          onChange={(e) => setProjectPath(e.target.value)}
        />
        <Input
          label="Initial prompt"
          name="prompt"
          placeholder="What should the agent do?"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />
        <label
          htmlFor="new-session-model"
          style={{ display: "block", padding: "8px 16px" }}
        >
          Model
          <select
            id="new-session-model"
            value={selectedModel}
            disabled={modelsLoading}
            onChange={(e) => setSelectedModel(e.target.value)}
            className="evener-input"
            style={{ display: "block", width: "100%", marginTop: "4px" }}
          >
            <option value="">Default model</option>
            {models.map((descriptor) => (
              <option key={modelKey(descriptor)} value={modelKey(descriptor)}>
                {descriptor.provider} / {descriptor.model}
              </option>
            ))}
          </select>
        </label>
      </div>
      {modelsError ? (
        <div
          className="evener-error"
          role="status"
          style={{ padding: "8px 16px", color: "var(--secondary)" }}
        >
          Model discovery unavailable; using the server default.
        </div>
      ) : null}
      {recentProjects.length > 0 ? (
        <div className="evener-list-group" style={{ padding: "0 16px" }}>
          <div
            style={{
              fontSize: "0.85em",
              color: "var(--secondary)",
              padding: "8px 0",
            }}
          >
            Recent projects
          </div>
          {recentProjects.map((p) => (
            <button
              key={p}
              type="button"
              className="evener-list-row"
              onClick={() => setProjectPath(p)}
              style={{
                display: "block",
                width: "100%",
                padding: "12px",
                textAlign: "left",
                border: "none",
                background: "transparent",
                color: "inherit",
                font: "inherit",
                cursor: "pointer",
                borderBottom: "1px solid var(--hairline)",
              }}
            >
              {p}
            </button>
          ))}
        </div>
      ) : null}
      {error ? (
        <div
          className="evener-error"
          role="alert"
          style={{ padding: "8px 16px", color: "var(--danger, #cc0000)" }}
        >
          {error}
        </div>
      ) : null}
      <div style={{ padding: "16px" }}>
        <Button
          variant="primary"
          disabled={!canStart}
          onClick={() => void handleStart()}
        >
          Start
        </Button>
      </div>
    </div>
  );
}

function modelKey(descriptor: ModelDescriptor): string {
  return `${descriptor.provider}\u0000${descriptor.model}`;
}
