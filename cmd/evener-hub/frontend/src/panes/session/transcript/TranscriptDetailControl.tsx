import type { Ref } from "react";
import { useState } from "react";
import { useIsMobile } from "../../../shell/useIsMobile";
import { transcriptDisplayStore, useTranscriptDisplayStore } from "../../../stores/transcriptDisplay";
import {
  advancedEnabledCount,
  contentSummary,
  type EffectiveConfigSources,
  resolveEffectiveConfig,
  type ViewportClass,
} from "../../../transcriptDisplay/config";
import { requireClass } from "../../../widgets/internal/requireClass";
import { Popover } from "../../../widgets/popover";
import { Sheet } from "../../../widgets/sheet";
import { TranscriptDetailEditor } from "./TranscriptDetailEditor";
import styles from "./transcriptDisplay.module.css";

export interface TranscriptDetailControlProps {
  layout: ViewportClass;
  onEditHubDefaults(): void;
  triggerRef?: Ref<HTMLButtonElement>;
}

const CLASS = {
  root: requireClass(styles.detailControl, "transcriptDisplay.module.css", "detailControl"),
  trigger: requireClass(styles.detailTrigger, "transcriptDisplay.module.css", "detailTrigger"),
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
  return extras === 0 ? `Detail: ${content}` : `Detail: ${content} · ${extras} advanced`;
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
}: DetailPanelProps) {
  const layoutName = layout === "desktop" ? "Desktop" : "Mobile";
  const statusMessages: string[] = [];
  if (hubLoading) statusMessages.push("Loading hub default…");
  if (hubSupport === "unsupported")
    statusMessages.push("This older hub does not support transcript display defaults. Local changes still work.");
  if (hubError !== null) statusMessages.push(`Hub default status: ${hubError}`);

  return (
    <div className={showTitle ? CLASS.content : CLASS.sheetContent}>
      {showTitle && <h2 className={CLASS.panelTitle}>Transcript display details</h2>}
      <p className={CLASS.scope}>{localExists ? `Local ${layoutName} view` : "Using hub default"}</p>
      {statusMessages.length > 0 && (
        <div className={CLASS.status} role="status" aria-live="polite">
          {statusMessages.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </div>
      )}
      {storageWarning !== null && (
        <p className={CLASS.warning} role="alert">
          {storageWarning}
        </p>
      )}
      <TranscriptDetailEditor value={effectiveConfig} compact onChange={onChange} />
      <div className={CLASS.actions}>
        {localExists && (
          <button type="button" onClick={onClearLocal}>
            Use hub default
          </button>
        )}
        <button type="button" onClick={onEditHubDefaults}>
          Edit hub defaults
        </button>
      </div>
    </div>
  );
}

export function TranscriptDetailControl({ layout, onEditHubDefaults, triggerRef }: TranscriptDetailControlProps) {
  const [open, setOpen] = useState(false);
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
    />
  );

  const trigger = (
    <button
      ref={triggerRef}
      type="button"
      className={CLASS.trigger}
      aria-expanded={open}
      aria-haspopup={isMobile ? "dialog" : "true"}
      onClick={() => setOpen((value) => !value)}
    >
      {triggerLabel}
    </button>
  );

  return (
    <div className={CLASS.root}>
      {isMobile ? (
        <>
          {trigger}
          <Sheet open={open} side="bottom" size="wide" onClose={close} title="Transcript display details">
            {panel}
          </Sheet>
        </>
      ) : (
        <Popover open={open} onClose={close} trigger={trigger} data-testid="transcript-detail-popover">
          <div className={CLASS.panel}>{panel}</div>
        </Popover>
      )}
    </div>
  );
}
