import type { RefObject } from "react";
import { useRef } from "react";
import { transcriptDisplayStore, useTranscriptDisplayStore } from "../../../stores/transcriptDisplay";
import { resolveEffectiveConfig, type ViewportClass } from "../../../transcriptDisplay/config";
import { Button, Dialog, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { TranscriptDetailEditor } from "./TranscriptDetailEditor";
import styles from "./transcriptDisplay.module.css";

export interface TranscriptDetailControlProps {
  open: boolean;
  onClose(): void;
  layout: ViewportClass;
  onEditHubDefaults(): void;
}

const CLASS = {
  panel: requireClass(styles.detailPanel, "transcriptDisplay.module.css", "detailPanel"),
  content: requireClass(styles.detailContent, "transcriptDisplay.module.css", "detailContent"),
  sheetContent: requireClass(styles.detailSheetContent, "transcriptDisplay.module.css", "detailSheetContent"),
  scope: requireClass(styles.detailScope, "transcriptDisplay.module.css", "detailScope"),
  status: requireClass(styles.detailStatus, "transcriptDisplay.module.css", "detailStatus"),
  warning: requireClass(styles.detailWarning, "transcriptDisplay.module.css", "detailWarning"),
  actions: requireClass(styles.detailActions, "transcriptDisplay.module.css", "detailActions"),
};

interface DetailPanelProps {
  layout: ViewportClass;
  localExists: boolean;
  effectiveConfig: ReturnType<typeof resolveEffectiveConfig>;
  hubLoading: boolean;
  hubError: string | null;
  hubSupport: "unknown" | "supported" | "unsupported";
  storageWarning: string | null;
  onChange(value: ReturnType<typeof resolveEffectiveConfig>): void;
  onClearLocal(): void;
  onEditHubDefaults(): void;
  showActions: boolean;
  mobile: boolean;
  editButtonRef: RefObject<HTMLButtonElement | null>;
}

interface DetailActionsProps {
  localExists: boolean;
  editButtonRef: RefObject<HTMLButtonElement | null>;
  onClearLocal(): void;
  onEditHubDefaults(): void;
}

function DetailActions({ localExists, editButtonRef, onClearLocal, onEditHubDefaults }: DetailActionsProps) {
  function clearLocal(): void {
    editButtonRef.current?.focus();
    onClearLocal();
  }

  return (
    <div className={CLASS.actions}>
      {localExists && (
        <Button size="sm" variant="secondary" onClick={clearLocal}>
          Use hub default
        </Button>
      )}
      <Button ref={editButtonRef} size="sm" variant="quiet" onClick={onEditHubDefaults}>
        Edit hub defaults
      </Button>
    </div>
  );
}

function DetailPanel({
  layout,
  localExists,
  effectiveConfig,
  hubLoading,
  hubError,
  hubSupport,
  storageWarning,
  onChange,
  onClearLocal,
  onEditHubDefaults,
  showActions,
  mobile,
  editButtonRef,
}: DetailPanelProps) {
  const layoutName = layout === "desktop" ? "Desktop" : "Mobile";
  const statusMessages: string[] = [];
  if (hubLoading) statusMessages.push("Loading hub default…");
  if (hubSupport === "unsupported")
    statusMessages.push("This older hub does not support transcript display defaults. Local changes still work.");
  const failureMessages = new Map<string, string>();
  if (hubError !== null) failureMessages.set(hubError, `Hub default status: ${hubError}`);
  if (storageWarning !== null && !failureMessages.has(storageWarning))
    failureMessages.set(storageWarning, storageWarning);

  return (
    <div
      className={mobile ? CLASS.sheetContent : `${CLASS.panel} ${CLASS.content}`}
      data-testid="transcript-detail-control"
    >
      <p className={CLASS.scope}>{localExists ? `Local ${layoutName} view` : "Using hub default"}</p>
      {statusMessages.length > 0 && (
        <div className={CLASS.status} role="status" aria-live="polite">
          {statusMessages.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </div>
      )}
      {failureMessages.size > 0 && (
        <div className={CLASS.warning} role="alert">
          {[...failureMessages.values()].map((message) => (
            <p key={message}>{message}</p>
          ))}
        </div>
      )}
      <TranscriptDetailEditor value={effectiveConfig} compact onChange={onChange} />
      {showActions && (
        <DetailActions
          localExists={localExists}
          editButtonRef={editButtonRef}
          onClearLocal={onClearLocal}
          onEditHubDefaults={onEditHubDefaults}
        />
      )}
    </div>
  );
}

export function TranscriptDetailControl({ open, onClose, layout, onEditHubDefaults }: TranscriptDetailControlProps) {
  const editButtonRef = useRef<HTMLButtonElement>(null);
  const isMobile = layout === "mobile";
  const local = useTranscriptDisplayStore((state) => state.local[layout]);
  const hub = useTranscriptDisplayStore((state) => state.hub[layout]);
  const hubLoading = useTranscriptDisplayStore((state) => state.hubLoading);
  const hubError = useTranscriptDisplayStore((state) => state.hubError);
  const hubSupport = useTranscriptDisplayStore((state) => state.hubSupport);
  const storageWarning = useTranscriptDisplayStore((state) => state.storageWarning);
  const effectiveConfig = resolveEffectiveConfig({ local, hub, layout });

  function editHubDefaults(): void {
    onClose();
    onEditHubDefaults();
  }

  const panel = (
    <DetailPanel
      layout={layout}
      localExists={local !== undefined}
      effectiveConfig={effectiveConfig}
      hubLoading={hubLoading}
      hubError={hubError}
      hubSupport={hubSupport}
      storageWarning={storageWarning}
      onChange={(next) => transcriptDisplayStore.getState().setLocal(layout, next)}
      onClearLocal={() => transcriptDisplayStore.getState().clearLocal(layout)}
      onEditHubDefaults={editHubDefaults}
      showActions={!isMobile}
      mobile={isMobile}
      editButtonRef={editButtonRef}
    />
  );

  const footer = (
    <DetailActions
      localExists={local !== undefined}
      editButtonRef={editButtonRef}
      onClearLocal={() => transcriptDisplayStore.getState().clearLocal(layout)}
      onEditHubDefaults={editHubDefaults}
    />
  );

  return isMobile ? (
    <Sheet open={open} side="bottom" size="wide" onClose={onClose} title="Verbosity" footer={footer}>
      {panel}
    </Sheet>
  ) : (
    <Dialog open={open} onClose={onClose} title="Verbosity">
      {panel}
    </Dialog>
  );
}
