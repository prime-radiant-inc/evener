import { type ContentLevel, resolveEffectiveConfig, type ViewportClass } from "@evener/appwire-client";
import type { RefObject } from "react";
import { useId, useRef } from "react";
import { setSessionDetailLevel, useSessionDetailLevel } from "../../../stores/sessionDetailLevel";
import { transcriptDisplayStore, useTranscriptDisplayStore } from "../../../stores/transcriptDisplay";
import { Button, Dialog, FormRow, Select, type SelectOption, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { PRESET_LEVEL_OPTIONS, TranscriptDetailEditor } from "./TranscriptDetailEditor";
import styles from "./transcriptDisplay.module.css";

export interface TranscriptDetailControlProps {
  open: boolean;
  onClose(): void;
  layout: ViewportClass;
  onEditHubDefaults(): void;
  /** The session this control was opened from. With one, the control also
   * offers a level for that session alone; without one it edits only the
   * default every session shows. */
  sessionRef?: string;
}

// The session picker's "no level of its own" choice. Not a ContentLevel, so
// it can never be stored as one.
const SESSION_DEFAULT = "default";

const SESSION_LEVEL_OPTIONS: SelectOption[] = [
  { value: SESSION_DEFAULT, label: "Same as default" },
  ...PRESET_LEVEL_OPTIONS,
];

function isPresetLevel(value: string): value is ContentLevel {
  return PRESET_LEVEL_OPTIONS.some((option) => option.value === value);
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
  /** The session's own level, when the control was opened from a session:
   * null while it shows the default. Undefined hides the session picker. */
  sessionLevel?: ContentLevel | null;
  onSessionLevelChange(level: ContentLevel | null): void;
  onChange(value: ReturnType<typeof resolveEffectiveConfig>): void;
  onClearLocal(): void;
  onEditHubDefaults(): void;
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
  sessionLevel,
  onSessionLevelChange,
  onChange,
  onClearLocal,
  onEditHubDefaults,
  editButtonRef,
}: DetailPanelProps) {
  const sessionLevelId = useId();
  const isMobile = layout === "mobile";
  const layoutName = layout === "desktop" ? "Desktop" : "Mobile";
  const statusMessages: { kind: "hub-loading" | "hub-unsupported"; text: string }[] = [];
  if (hubLoading) statusMessages.push({ kind: "hub-loading", text: "Loading hub default…" });
  if (hubSupport === "unsupported")
    statusMessages.push({
      kind: "hub-unsupported",
      text: "This older hub does not support transcript display defaults. Local changes still work.",
    });
  const failureMessages = new Map<string, string>();
  if (hubError !== null) failureMessages.set(hubError, `Hub default status: ${hubError}`);
  if (storageWarning !== null && !failureMessages.has(storageWarning))
    failureMessages.set(storageWarning, storageWarning);

  return (
    <div
      className={isMobile ? CLASS.sheetContent : `${CLASS.panel} ${CLASS.content}`}
      data-testid="transcript-detail-control"
    >
      {sessionLevel !== undefined && (
        <FormRow
          label="This session"
          htmlFor={sessionLevelId}
          help="Only this session, in this browser. Everything below is the default, which a session left on Same as default shows."
        >
          <Select
            id={sessionLevelId}
            value={sessionLevel ?? SESSION_DEFAULT}
            options={SESSION_LEVEL_OPTIONS}
            onChange={(event) => {
              const next = event.target.value;
              onSessionLevelChange(isPresetLevel(next) ? next : null);
            }}
          />
        </FormRow>
      )}
      <p className={CLASS.scope} data-testid="transcript-detail-scope" data-scope={localExists ? "local" : "hub"}>
        {localExists ? `Local ${layoutName} view` : "Using hub default"}
      </p>
      {statusMessages.length > 0 && (
        <div className={CLASS.status} role="status" aria-live="polite">
          {statusMessages.map((message) => (
            <p key={message.kind} data-kind={message.kind}>
              {message.text}
            </p>
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
      {!isMobile && (
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

export function TranscriptDetailControl({
  open,
  onClose,
  layout,
  onEditHubDefaults,
  sessionRef,
}: TranscriptDetailControlProps) {
  const sessionLevel = useSessionDetailLevel(sessionRef);
  const editButtonRef = useRef<HTMLButtonElement>(null);
  const isMobile = layout === "mobile";
  const local = useTranscriptDisplayStore((state) => state.local[layout]);
  const hub = useTranscriptDisplayStore((state) => state.hub[layout]);
  const hubLoading = useTranscriptDisplayStore((state) => state.hubLoading);
  const hubError = useTranscriptDisplayStore((state) => state.hubError);
  const hubSupport = useTranscriptDisplayStore((state) => state.hubSupport);
  const storageWarning = useTranscriptDisplayStore((state) => state.storageWarning);
  const effectiveConfig = resolveEffectiveConfig({ local, hub, layout });
  const localExists = local !== undefined;

  function setLocal(next: ReturnType<typeof resolveEffectiveConfig>): void {
    transcriptDisplayStore.getState().setLocal(layout, next);
  }

  function clearLocal(): void {
    transcriptDisplayStore.getState().clearLocal(layout);
  }

  function editHubDefaults(): void {
    onClose();
    onEditHubDefaults();
  }

  const panel = (
    <DetailPanel
      layout={layout}
      localExists={localExists}
      effectiveConfig={effectiveConfig}
      hubLoading={hubLoading}
      hubError={hubError}
      hubSupport={hubSupport}
      storageWarning={storageWarning}
      sessionLevel={sessionRef === undefined ? undefined : sessionLevel}
      onSessionLevelChange={(level) => {
        if (sessionRef !== undefined) setSessionDetailLevel(sessionRef, level, effectiveConfig);
      }}
      onChange={setLocal}
      onClearLocal={clearLocal}
      onEditHubDefaults={editHubDefaults}
      editButtonRef={editButtonRef}
    />
  );

  const footer = (
    <DetailActions
      localExists={localExists}
      editButtonRef={editButtonRef}
      onClearLocal={clearLocal}
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
