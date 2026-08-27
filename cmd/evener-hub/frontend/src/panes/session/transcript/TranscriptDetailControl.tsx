import type { Ref, RefObject } from "react";
import { useId, useRef, useState } from "react";
import { useIsMobile } from "../../../shell/useIsMobile";
import { transcriptDisplayStore, useTranscriptDisplayStore } from "../../../stores/transcriptDisplay";
import {
  advancedEnabledCount,
  contentSummary,
  type EffectiveConfigSources,
  resolveEffectiveConfig,
  type ViewportClass,
} from "../../../transcriptDisplay/config";
import { Button, Popover, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { TranscriptDetailEditor } from "./TranscriptDetailEditor";
import styles from "./transcriptDisplay.module.css";

export interface TranscriptDetailControlProps {
  layout: ViewportClass;
  onEditHubDefaults(): void;
  triggerRef?: Ref<HTMLButtonElement>;
}

const CLASS = {
  root: requireClass(styles.detailControl, "transcriptDisplay.module.css", "detailControl"),
  panel: requireClass(styles.detailPanel, "transcriptDisplay.module.css", "detailPanel"),
  panelTitle: requireClass(styles.detailPanelTitle, "transcriptDisplay.module.css", "detailPanelTitle"),
  content: requireClass(styles.detailContent, "transcriptDisplay.module.css", "detailContent"),
  sheetContent: requireClass(styles.detailSheetContent, "transcriptDisplay.module.css", "detailSheetContent"),
  scope: requireClass(styles.detailScope, "transcriptDisplay.module.css", "detailScope"),
  status: requireClass(styles.detailStatus, "transcriptDisplay.module.css", "detailStatus"),
  warning: requireClass(styles.detailWarning, "transcriptDisplay.module.css", "detailWarning"),
  actions: requireClass(styles.detailActions, "transcriptDisplay.module.css", "detailActions"),
};

function summaryFor(sources: EffectiveConfigSources): string {
  const normalized = resolveEffectiveConfig(sources);
  const extras = advancedEnabledCount(normalized);
  const content = contentSummary(normalized.content);
  return extras === 0 ? `Detail: ${content}` : `Detail: ${content} · ${extras} extras`;
}

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
  showTitle: boolean;
  headingId: string;
  showActions: boolean;
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
  showTitle,
  headingId,
  showActions,
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
    <div className={showTitle ? CLASS.content : CLASS.sheetContent}>
      {showTitle && (
        <h2 id={headingId} className={CLASS.panelTitle}>
          Transcript display details
        </h2>
      )}
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

export function TranscriptDetailControl({ layout, onEditHubDefaults, triggerRef }: TranscriptDetailControlProps) {
  const [open, setOpen] = useState(false);
  const headingId = useId();
  const editButtonRef = useRef<HTMLButtonElement>(null);
  const isMobile = useIsMobile();
  const local = useTranscriptDisplayStore((state) => state.local[layout]);
  const hub = useTranscriptDisplayStore((state) => state.hub[layout]);
  const hubLoading = useTranscriptDisplayStore((state) => state.hubLoading);
  const hubError = useTranscriptDisplayStore((state) => state.hubError);
  const hubSupport = useTranscriptDisplayStore((state) => state.hubSupport);
  const storageWarning = useTranscriptDisplayStore((state) => state.storageWarning);
  const effectiveConfig = resolveEffectiveConfig({ local, hub, layout });
  const triggerLabel = summaryFor({ local, hub, layout });

  function close(): void {
    setOpen(false);
  }

  function editHubDefaults(): void {
    close();
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
      showTitle={!isMobile}
      headingId={headingId}
      showActions={!isMobile}
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

  const trigger = (
    <Button
      ref={triggerRef}
      size="sm"
      variant="secondary"
      aria-expanded={open}
      aria-haspopup="dialog"
      onClick={() => setOpen((current) => !current)}
    >
      {triggerLabel}
    </Button>
  );

  return (
    <div className={CLASS.root}>
      {isMobile ? (
        <>
          {trigger}
          <Sheet
            open={open}
            side="bottom"
            size="wide"
            onClose={close}
            title="Transcript display details"
            footer={footer}
          >
            {panel}
          </Sheet>
        </>
      ) : (
        <Popover
          open={open}
          onClose={close}
          closeOnScroll={false}
          trigger={trigger}
          data-testid="transcript-detail-popover"
        >
          <div className={CLASS.panel} role="dialog" aria-modal="false" aria-labelledby={headingId}>
            {panel}
          </div>
        </Popover>
      )}
    </div>
  );
}
