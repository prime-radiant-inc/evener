import { useEffect, useRef, useState } from "react";
import type { ModelCatalog, ModelCatalogEntry, PathFieldPanelProps } from "../../widgets";
import { ModelCatalogPanel, PathFieldPanel, Sheet } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./MobileSettingRows.module.css";

export interface MobilePickerOption {
  value: string;
  label: string;
}

export interface MobileSettingRowsProps {
  harness: string;
  harnessOptions: MobilePickerOption[];
  onHarnessChange: (value: string) => void;
  model: string;
  modelDisplay: string;
  modelRequired: boolean;
  loadCatalog: () => Promise<ModelCatalog>;
  onModelChange: (value: string) => void;
  cwd: string;
  onCwdChange: (value: string) => void;
  complete: PathFieldPanelProps["complete"];
  listRecents: NonNullable<PathFieldPanelProps["listRecents"]>;
  fallbackDir: string;
  onCwdPanelClose: (value: string) => void;
  branch: string;
  reasoningEffort: string;
  reasoningOptions: MobilePickerOption[];
  reasoningDisabled: boolean;
  onReasoningChange: (value: string) => void;
  accessMode: string;
  accessOptions: MobilePickerOption[];
  onAccessChange: (value: string) => void;
}

type PickerName = "Harness" | "Model" | "Working directory" | "Reasoning effort" | "Access mode";

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
  modelPanel: requireClass(styles.modelPanel, "MobileSettingRows.module.css", "modelPanel"),
};

interface MobileSettingRowProps {
  label: string;
  value: string;
  onClick?: () => void;
  expanded?: boolean;
  disabled?: boolean;
}

function MobileSettingRow({ label, value, onClick, expanded = false, disabled = false }: MobileSettingRowProps) {
  const content = (
    <>
      <span className={CLASS.rowLabel}>{label}</span>
      <span className={CLASS.rowValue} title={value}>
        {value}
      </span>
      {onClick !== undefined && (
        <span className={CLASS.caret} aria-hidden="true">
          ›
        </span>
      )}
    </>
  );

  return (
    <div
      className={`${CLASS.row} ${onClick === undefined ? CLASS.readOnly : ""}`}
      data-testid="mobile-spawn-row"
      data-label={label}
    >
      {onClick === undefined ? (
        content
      ) : (
        <button
          type="button"
          className={CLASS.rowButton}
          aria-haspopup="dialog"
          aria-expanded={expanded}
          aria-label={`${label}: ${value}`}
          onClick={onClick}
          disabled={disabled}
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
  model,
  modelDisplay,
  modelRequired,
  loadCatalog,
  onModelChange,
  cwd,
  onCwdChange,
  complete,
  listRecents,
  fallbackDir,
  onCwdPanelClose,
  branch,
  reasoningEffort,
  reasoningOptions,
  reasoningDisabled,
  onReasoningChange,
  accessMode,
  accessOptions,
  onAccessChange,
}: MobileSettingRowsProps) {
  const [openPicker, setOpenPicker] = useState<PickerName | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const lastTriggerRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (openPicker !== null || lastTriggerRef.current === null) return;
    lastTriggerRef.current.focus();
  }, [openPicker]);

  useEffect(() => {
    if (openPicker !== "Model") return;
    let active = true;
    setCatalogLoading(true);
    setCatalogError(null);
    loadCatalog().then(
      (next) => {
        if (!active) return;
        setCatalog(next);
        setCatalogLoading(false);
      },
      (error: unknown) => {
        if (!active) return;
        setCatalogError(error instanceof Error ? error.message : String(error));
        setCatalogLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, [loadCatalog, openPicker]);

  function closePicker(): void {
    if (openPicker === "Working directory") onCwdPanelClose(cwd);
    setOpenPicker(null);
  }

  function open(name: PickerName): void {
    const active = document.activeElement;
    if (active instanceof HTMLButtonElement) lastTriggerRef.current = active;
    setOpenPicker(name);
  }

  function changeModel(entry: ModelCatalogEntry): void {
    onModelChange(`${entry.provider}/${entry.model}`);
    setOpenPicker(null);
  }

  const harnessLabel = harnessOptions.find((option) => option.value === harness)?.label ?? harness;
  const reasoningLabel = reasoningOptions.find((option) => option.value === reasoningEffort)?.label ?? "(default)";
  const accessLabel = accessOptions.find((option) => option.value === accessMode)?.label ?? "(default)";

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
          label="Model"
          value={modelDisplay}
          onClick={() => open("Model")}
          expanded={openPicker === "Model"}
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
      </div>

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

      <Sheet open={openPicker === "Model"} side="bottom" onClose={closePicker} title="Choose model">
        <div className={CLASS.sheetBody}>
          {!modelRequired && (
            <button
              type="button"
              className={CLASS.option}
              aria-pressed={model === ""}
              onClick={() => {
                onModelChange("");
                closePicker();
              }}
            >
              <span>Use default</span>
              {model === "" && <span className={CLASS.selection}>Selected</span>}
            </button>
          )}
          <div className={CLASS.modelPanel}>
            <ModelCatalogPanel
              loading={catalogLoading}
              error={catalogError}
              catalog={catalog}
              value={model}
              onPick={changeModel}
            />
          </div>
        </div>
      </Sheet>

      <Sheet
        open={openPicker === "Working directory"}
        side="bottom"
        onClose={closePicker}
        title="Choose working directory"
      >
        <div className={CLASS.sheetBody}>
          <PathFieldPanel
            kind="dir"
            value={cwd}
            onChange={onCwdChange}
            onCommit={(value) => {
              onCwdChange(value);
              closePicker();
            }}
            complete={complete}
            listRecents={listRecents}
            fallbackDir={fallbackDir}
          />
        </div>
      </Sheet>
    </>
  );
}
