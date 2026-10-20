import {
  configFingerprint,
  type HubTranscriptDisplayDefault,
  StoreMessageError,
  shippedDefault,
  storeErrorText,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { isMobileViewport } from "../../../shell/useIsMobile";
import {
  initTranscriptDisplay,
  transcriptDisplayStore,
  useTranscriptDisplayStore,
} from "../../../stores/transcriptDisplay";
import { Button, InlineNotice, SegmentedControl, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { TranscriptDisplayCard } from "./TranscriptDisplayCard";
import styles from "./transcript.module.css";

const CLASS = {
  root: requireClass(styles.root, "transcript.module.css", "root"),
  intro: requireClass(styles.intro, "transcript.module.css", "intro"),
  cards: requireClass(styles.cards, "transcript.module.css", "cards"),
  status: requireClass(styles.status, "transcript.module.css", "status"),
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
  const [layout, setLayout] = useState<Layout>(() => (isMobileViewport() ? "mobile" : "desktop"));
  const hub = useTranscriptDisplayStore((state) => state.hub);
  const drafts = useTranscriptDisplayStore((state) => state.drafts);
  const local = useTranscriptDisplayStore((state) => state.local);
  const hubLoading = useTranscriptDisplayStore((state) => state.hubLoading);
  const hubError = useTranscriptDisplayStore((state) => state.hubError);
  const hubErrors = useTranscriptDisplayStore((state) => state.hubErrors);
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
      const canonical = await transcriptDisplayStore.getState().patchHubDefault(layout, config);
      if (saveOperations.current[layout] !== operation) return;
      const storeState = transcriptDisplayStore.getState();
      if (configFingerprint(canonical.config) !== configFingerprint(config) || storeState.drafts[layout] !== undefined)
        throw new StoreMessageError("Hub did not acknowledge this transcript display default.");
      // patchHubDefault returns only a fully validated canonical response for
      // this layout. Do not inspect store-wide errors: another layout may be
      // settling concurrently.
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
          error: storeErrorText(error),
        },
      }));
    }
  }

  function retry(layout: Layout): void {
    const config = pending[layout]?.config;
    if (config !== undefined) void save(layout, config);
    else void transcriptDisplayStore.getState().refreshHubDefaults();
  }

  // A write failure rides hubErrors[layout]; only a failed hub read sets the
  // store-wide hubError, so a set hubError is always the read's to report.
  const serverIssue = hubError !== null;
  const disabled = hubSupport !== "supported" || hubLoading || serverIssue;
  let status: { reason: "hub-unknown" | "unsupported" | "loading"; text: string } | undefined;
  if (hubSupport === "unknown") {
    status = { reason: "hub-unknown", text: "Waiting for the hub connection to report transcript display support." };
  } else if (hubSupport === "unsupported") {
    status = {
      reason: "unsupported",
      text: "This older hub does not support synced transcript defaults. Update the hub to edit Desktop and Mobile defaults.",
    };
  } else if (hubLoading) {
    status = { reason: "loading", text: "Loading transcript display defaults from the hub." };
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>
        Transcript display defaults sync to devices paired with this hub. Live transcript choices remain browser-local.
      </p>

      {storageWarning !== null && (
        <InlineNotice tone="danger">Browser-local storage warning: {storageWarning}</InlineNotice>
      )}
      {status !== undefined && (
        <p className={CLASS.status} role="status" aria-live="polite" data-reason={status.reason}>
          {status.text}
        </p>
      )}
      {serverIssue && (
        <InlineNotice
          tone="danger"
          action={
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void transcriptDisplayStore.getState().refreshHubDefaults()}
            >
              Retry
            </Button>
          }
        >
          Could not load transcript display defaults: {hubError}
        </InlineNotice>
      )}

      <SegmentedControl<Layout>
        label="Device layout"
        value={layout}
        options={[
          { value: "desktop", label: "Desktop" },
          { value: "mobile", label: "Mobile" },
        ]}
        onChange={setLayout}
      />
      <div className={CLASS.cards}>
        <TranscriptDisplayCard
          key={layout}
          layout={layout}
          confirmed={confirmedFor(layout, hub)}
          draft={drafts[layout]}
          localOverride={local[layout]}
          saveState={pending[layout]?.state ?? "idle"}
          error={pending[layout]?.error ?? hubErrors[layout]}
          disabled={disabled}
          onChange={(config) => void save(layout, config)}
          onRetry={() => retry(layout)}
          onClearLocal={() => transcriptDisplayStore.getState().clearLocal(layout)}
        />
      </div>
    </div>
  );
}
