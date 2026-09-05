// The phone's configuration list for the spawn pane: one tappable row per
// setting, each opening a bottom Sheet.
//
// Model is deliberately NOT here. It is set from the prompt card itself, by
// the same ModelSwitchTrigger the session composer carries (issue #198) - so
// the one act of choosing a model looks and behaves the same wherever it
// happens, instead of being a bespoke sheet on this surface and a popover
// picker on every other.
import { useEffect, useRef, useState } from "react";
import type { PathFieldPanelProps } from "../../widgets";
import { Button, Sheet } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./MobileSettingRows.module.css";
import { PluginSelectionPanel } from "./PluginSelectionPanel";
import { type PluginSelectionState, selectedPluginNames } from "./pluginSelectionState";
import type { PluginPreviewLoadState } from "./usePluginPreview";
import { WorkingDirectoryPicker, type WorkingDirectoryPickerProps } from "./WorkingDirectoryPicker";

export interface MobilePickerOption {
  value: string;
  label: string;
}

export interface MobileSettingRowsProps {
  harness: string;
  harnessOptions: MobilePickerOption[];
  onHarnessChange: (value: string) => void;
  cwd: string;
  onCwdChange: (value: string) => void;
  complete: PathFieldPanelProps["complete"];
  listRecents: NonNullable<PathFieldPanelProps["listRecents"]>;
  fallbackDir: string;
  validatePath: WorkingDirectoryPickerProps["validatePath"];
  createDirectory: WorkingDirectoryPickerProps["createDirectory"];
  onCwdPanelClose: (value: string) => void;
  branch: string;
  reasoningEffort: string;
  reasoningOptions: MobilePickerOption[];
  reasoningDisabled: boolean;
  onReasoningChange: (value: string) => void;
  accessMode: string;
  accessOptions: MobilePickerOption[];
  onAccessChange: (value: string) => void;
  pluginPreview: PluginPreviewLoadState;
  pluginSelection: PluginSelectionState;
  pluginsSupported?: boolean;
  onPluginSelectionChange: (next: PluginSelectionState) => void;
  onPluginRetry: () => void;
}

type PickerName = "Harness" | "Working directory" | "Reasoning effort" | "Access mode" | "Plugins";

const CLASS = {
  config: requireClass(styles.config, "MobileSettingRows.module.css", "config"),
  row: requireClass(styles.row, "MobileSettingRows.module.css", "row"),
  rowButton: requireClass(styles.rowButton, "MobileSettingRows.module.css", "rowButton"),
  rowLabel: requireClass(styles.rowLabel, "MobileSettingRows.module.css", "rowLabel"),
  rowValue: requireClass(styles.rowValue, "MobileSettingRows.module.css", "rowValue"),
  caret: requireClass(styles.caret, "MobileSettingRows.module.css", "caret"),
  readOnly: requireClass(styles.readOnly, "MobileSettingRows.module.css", "readOnly"),
  sheetBody: requireClass(styles.sheetBody, "MobileSettingRows.module.css", "sheetBody"),
  optionList: requireClass(styles.optionList, "MobileSettingRows.module.css", "optionList"),
  option: requireClass(styles.option, "MobileSettingRows.module.css", "option"),
  optionSelected: requireClass(styles.optionSelected, "MobileSettingRows.module.css", "optionSelected"),
  selection: requireClass(styles.selection, "MobileSettingRows.module.css", "selection"),
};

interface MobileSettingRowProps {
  label: string;
  value: string;
  onClick?: () => void;
  expanded?: boolean;
  disabled?: boolean;
}

function MobileSettingRow({ label, value, onClick, expanded = false, disabled = false }: MobileSettingRowProps) {
  const interactive = onClick !== undefined && !disabled;
  const content = (
    <>
      <span className={CLASS.rowLabel}>{label}</span>
      <span className={CLASS.rowValue} title={value}>
        {value}
      </span>
      {interactive && (
        <span className={CLASS.caret} aria-hidden="true">
          ›
        </span>
      )}
    </>
  );

  return (
    <div
      className={`${CLASS.row} ${interactive ? "" : CLASS.readOnly}`}
      data-testid="mobile-spawn-row"
      data-label={label}
    >
      {!interactive ? (
        content
      ) : (
        <button
          type="button"
          className={CLASS.rowButton}
          aria-haspopup="dialog"
          aria-expanded={expanded}
          aria-label={`${label}: ${value}`}
          onClick={onClick}
        >
          {content}
        </button>
      )}
    </div>
  );
}

function OptionSheet({
  name,
  value,
  options,
  open,
  onClose,
  onChange,
}: {
  name: PickerName;
  value: string;
  options: MobilePickerOption[];
  open: boolean;
  onClose: () => void;
  onChange: (value: string) => void;
}) {
  return (
    <Sheet open={open} side="bottom" onClose={onClose} title={`Choose ${name.toLowerCase()}`}>
      <div className={CLASS.sheetBody}>
        <div className={CLASS.optionList}>
          {options.map((option) => {
            const selected = option.value === value;
            return (
              <button
                key={option.value}
                type="button"
                className={`${CLASS.option} ${selected ? CLASS.optionSelected : ""}`}
                aria-pressed={selected}
                onClick={() => {
                  onChange(option.value);
                  onClose();
                }}
              >
                <span>{option.label}</span>
                {selected && <span className={CLASS.selection}>Selected</span>}
              </button>
            );
          })}
        </div>
      </div>
    </Sheet>
  );
}

export function MobileSettingRows({
  harness,
  harnessOptions,
  onHarnessChange,
  cwd,
  onCwdChange,
  complete,
  listRecents,
  fallbackDir,
  validatePath,
  createDirectory,
  onCwdPanelClose,
  branch,
  reasoningEffort,
  reasoningOptions,
  reasoningDisabled,
  onReasoningChange,
  accessMode,
  accessOptions,
  onAccessChange,
  pluginPreview,
  pluginSelection,
  pluginsSupported = true,
  onPluginSelectionChange,
  onPluginRetry,
}: MobileSettingRowsProps) {
  const [openPicker, setOpenPicker] = useState<PickerName | null>(null);
  const [pluginDraft, setPluginDraft] = useState<PluginSelectionState>(pluginSelection);
  const lastTriggerRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (openPicker !== null || lastTriggerRef.current === null) return;
    lastTriggerRef.current.focus();
  }, [openPicker]);

  function closePicker(): void {
    if (openPicker === "Plugins") setPluginDraft(pluginSelection);
    setOpenPicker(null);
  }

  function finishPlugins(apply: boolean): void {
    if (apply) onPluginSelectionChange(pluginDraft);
    else setPluginDraft(pluginSelection);
    setOpenPicker(null);
  }

  function open(name: PickerName): void {
    const active = document.activeElement;
    if (active instanceof HTMLButtonElement) lastTriggerRef.current = active;
    setOpenPicker(name);
  }

  const harnessLabel = harnessOptions.find((option) => option.value === harness)?.label ?? harness;
  const reasoningLabel = reasoningOptions.find((option) => option.value === reasoningEffort)?.label ?? "(default)";
  const accessLabel = accessOptions.find((option) => option.value === accessMode)?.label ?? "(default)";
  const pluginResponse =
    pluginPreview.status === "ready" || pluginPreview.status === "error" ? pluginPreview.response : undefined;
  const pluginSummary =
    pluginPreview.status === "loading"
      ? "Inspecting plugins…"
      : pluginPreview.status === "error"
        ? "Couldn't inspect plugins"
        : `${selectedPluginNames(pluginSelection, pluginPreview.response).length} of ${
            pluginPreview.response.plugins.length
          }`;

  return (
    <>
      <div className={CLASS.config} data-testid="mobile-spawn-config">
        <MobileSettingRow
          label="Harness"
          value={harnessLabel}
          onClick={() => open("Harness")}
          expanded={openPicker === "Harness"}
        />
        <MobileSettingRow
          label="Working directory"
          value={cwd === "" ? "(default)" : cwd}
          onClick={() => open("Working directory")}
          expanded={openPicker === "Working directory"}
        />
        <MobileSettingRow label="Branch" value={branch === "" ? "No branch detected" : branch} />
        <MobileSettingRow
          label="Reasoning effort"
          value={reasoningLabel}
          onClick={() => open("Reasoning effort")}
          expanded={openPicker === "Reasoning effort"}
          disabled={reasoningDisabled}
        />
        <MobileSettingRow
          label="Access mode"
          value={accessLabel}
          onClick={() => open("Access mode")}
          expanded={openPicker === "Access mode"}
        />
        {pluginsSupported && (
          <MobileSettingRow
            label="Plugins"
            value={pluginSummary}
            onClick={() => {
              if (pluginResponse) setPluginDraft(pluginSelection);
              open("Plugins");
            }}
            expanded={openPicker === "Plugins"}
            disabled={!pluginResponse}
          />
        )}
      </div>
      {pluginsSupported && pluginPreview.status === "error" && (
        <div className={CLASS.sheetBody} role="status">
          <span>Couldn't inspect plugins</span>{" "}
          <Button variant="quiet" size="xs" type="button" onClick={onPluginRetry}>
            Retry
          </Button>
        </div>
      )}

      <OptionSheet
        name="Harness"
        value={harness}
        options={harnessOptions}
        open={openPicker === "Harness"}
        onClose={closePicker}
        onChange={onHarnessChange}
      />
      <OptionSheet
        name="Reasoning effort"
        value={reasoningEffort}
        options={reasoningOptions}
        open={openPicker === "Reasoning effort"}
        onClose={closePicker}
        onChange={onReasoningChange}
      />
      <OptionSheet
        name="Access mode"
        value={accessMode}
        options={accessOptions}
        open={openPicker === "Access mode"}
        onClose={closePicker}
        onChange={onAccessChange}
      />

      {openPicker === "Working directory" && (
        <WorkingDirectoryPicker
          key={cwd}
          value={cwd}
          fallbackDir={fallbackDir}
          complete={complete}
          listRecents={listRecents}
          validatePath={validatePath}
          createDirectory={createDirectory}
          onClose={closePicker}
          onPick={(path) => {
            onCwdChange(path);
            onCwdPanelClose(path);
            closePicker();
          }}
        />
      )}

      <Sheet
        open={pluginsSupported && openPicker === "Plugins" && pluginResponse !== undefined}
        side="bottom"
        size="wide"
        onClose={() => finishPlugins(false)}
        title="Plugins for this session"
        footer={
          <div className={CLASS.sheetBody}>
            <Button variant="quiet" size="sm" type="button" onClick={() => finishPlugins(false)}>
              Cancel
            </Button>
            <Button variant="primary" size="sm" type="button" onClick={() => finishPlugins(true)}>
              Done
            </Button>
          </div>
        }
      >
        <div className={CLASS.sheetBody}>
          <p>Load only selected plugins. This choice applies to this session.</p>
          <PluginSelectionPanel
            preview={pluginResponse ?? { plugins: [] }}
            selection={pluginDraft}
            removeOnly={pluginPreview.status === "error"}
            onSelectionChange={setPluginDraft}
            onRetry={onPluginRetry}
          />
        </div>
      </Sheet>
    </>
  );
}
