import { useEffect, useRef, useState } from "react";
import {
  initTranscriptDisplay,
  transcriptDisplayStore,
  useTranscriptDisplayStore,
} from "../../../stores/transcriptDisplay";
import {
  configFingerprint,
  type HubTranscriptDisplayDefault,
  shippedDefault,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { Button } from "../../../widgets/button";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useToasts } from "../../../widgets/toast";
import { TranscriptDisplayCard } from "./TranscriptDisplayCard";
import styles from "./transcript.module.css";

const CLASS = {
  root: requireClass(styles.root, "transcript.module.css", "root"),
  intro: requireClass(styles.intro, "transcript.module.css", "intro"),
  cards: requireClass(styles.cards, "transcript.module.css", "cards"),
  status: requireClass(styles.status, "transcript.module.css", "status"),
  error: requireClass(styles.error, "transcript.module.css", "error"),
};

type Layout = "desktop" | "mobile";
type SaveState = "idle" | "saving" | "error";

interface PendingSave {
  state: SaveState;
  config?: TranscriptDisplayConfigV1;
  error?: string;
}

type PendingSaves = Partial<Record<Layout, PendingSave>>;

function confirmedFor(
  layout: Layout,
  hub: Partial<Record<Layout, HubTranscriptDisplayDefault>>,
): HubTranscriptDisplayDefault {
  return hub[layout] ?? shippedDefault(layout);
}

/**
 * Settings -> Transcript display. This section edits hub defaults only; the
 * active browser-local view remains a separate layer and is called out on each
 * card when it is present.
 */
export function TranscriptSection() {
  const hub = useTranscriptDisplayStore((state) => state.hub);
  const drafts = useTranscriptDisplayStore((state) => state.drafts);
  const local = useTranscriptDisplayStore((state) => state.local);
  const hubLoading = useTranscriptDisplayStore((state) => state.hubLoading);
  const hubError = useTranscriptDisplayStore((state) => state.hubError);
  const storageWarning = useTranscriptDisplayStore((state) => state.storageWarning);
  const hubSupport = useTranscriptDisplayStore((state) => state.hubSupport);
  const [pending, setPending] = useState<PendingSaves>({});
  const saveOperations = useRef<Partial<Record<Layout, number>>>({});
  const { push } = useToasts();

  useEffect(() => {
    initTranscriptDisplay();
    void transcriptDisplayStore.getState().refreshHubDefaults();
  }, []);

  async function save(layout: Layout, config: TranscriptDisplayConfigV1): Promise<void> {
    const operation = (saveOperations.current[layout] ?? 0) + 1;
    saveOperations.current[layout] = operation;
    setPending((current) => ({ ...current, [layout]: { state: "saving", config } }));
    try {
      await transcriptDisplayStore.getState().patchHubDefault(layout, config);
      if (saveOperations.current[layout] !== operation) return;
      // Unsupported hubs return without a rejected promise so the store can
      // remain usable by local-view consumers. A Settings mutation must not
      // claim success in that case.
      const state = transcriptDisplayStore.getState();
      const confirmed = state.hub[layout] ?? shippedDefault(layout);
      const acknowledged =
        state.hubSupport === "supported" &&
        state.hubError === null &&
        state.drafts[layout] === undefined &&
        configFingerprint(confirmed.config) === configFingerprint(config);
      if (!acknowledged) {
        throw new Error(state.hubError ?? "Hub did not acknowledge this transcript display default.");
      }
      setPending((current) => ({ ...current, [layout]: { state: "idle" } }));
      // A success toast is deliberately after the request and canonical
      // response have settled; a draft is never described as saved.
      push("success", "Settings saved");
    } catch (error) {
      if (saveOperations.current[layout] !== operation) return;
      const currentDraft = transcriptDisplayStore.getState().drafts[layout];
      setPending((current) => ({
        ...current,
        [layout]: {
          state: "error",
          // If another writer left a newer draft in the store, preserve that
          // draft for Retry rather than replacing it with this stale request.
          config: currentDraft ?? config,
          error: error instanceof Error ? error.message : String(error),
        },
      }));
    }
  }

  function retry(layout: Layout): void {
    const config = pending[layout]?.config;
    if (config !== undefined) void save(layout, config);
    else void transcriptDisplayStore.getState().refreshHubDefaults();
  }

  const serverIssue = hubError !== null && !Object.values(pending).some((state) => state?.state === "error");
  const disabled = hubSupport !== "supported" || hubLoading || serverIssue;
  const status =
    hubSupport === "unknown"
      ? "Waiting for the hub connection to report transcript display support."
      : hubSupport === "unsupported"
        ? "This older hub does not support synced transcript defaults. Update the hub to edit Desktop and Mobile defaults."
        : hubLoading
          ? "Loading transcript display defaults from the hub."
          : undefined;

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Transcript display defaults are stored by this hub.</p>
      <p className={CLASS.intro}>Hub defaults sync to devices paired with this hub.</p>
      <p className={CLASS.intro}>A live transcript choice is browser-local and does not change another machine.</p>

      {storageWarning !== null && (
        <p className={CLASS.error} role="alert">
          Browser-local storage warning: {storageWarning}
        </p>
      )}
      {status !== undefined && (
        <p className={CLASS.status} role="status" aria-live="polite">
          {status}
        </p>
      )}
      {serverIssue && (
        <div className={CLASS.error} role="alert">
          <span>Could not load transcript display defaults: {hubError}</span>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void transcriptDisplayStore.getState().refreshHubDefaults()}
          >
            Retry
          </Button>
        </div>
      )}

      <div className={CLASS.cards}>
        {(["desktop", "mobile"] as const).map((layout) => {
          const state = pending[layout];
          return (
            <TranscriptDisplayCard
              key={layout}
              layout={layout}
              confirmed={confirmedFor(layout, hub)}
              draft={drafts[layout]}
              localOverride={local[layout]}
              saveState={state?.state ?? "idle"}
              error={state?.error}
              disabled={disabled}
              onChange={(config) => void save(layout, config)}
              onRetry={() => retry(layout)}
            />
          );
        })}
      </div>
    </div>
  );
}
