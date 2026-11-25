// The phone's configuration list for the spawn pane: one tappable row per
// setting, each opening a bottom Sheet.
//
// Model AND effort are deliberately NOT here. They are set from the prompt
// card itself, by the same ModelSwitchTrigger the session composer carries
// (issue #198) and the same quiet effort control - so the acts of choosing a
// model and an effort look and behave the same wherever they happen, instead
// of being bespoke sheets on this surface and different controls on every
// other.

import type { AppwireClientLike, PluginPreviewLoadState } from "@evener/appwire-client";
import { type PluginSelectionState, selectedPluginNames } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import {
  Button,
  DirectoryPicker,
  type DirectoryPickerProps,
  InlineNotice,
  ListRow,
  RadioGroup,
  Sheet,
  useFocusRehome,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./MobileSettingRows.module.css";
import { PluginSelectionPanel } from "./PluginSelectionPanel";
import { type SpawnHostOption, SpawnHostSelection } from "./SpawnHostSelection";

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
  complete: DirectoryPickerProps["complete"];
  listRecents: NonNullable<DirectoryPickerProps["listRecents"]>;
  fallbackDir: string;
  validatePath: DirectoryPickerProps["validatePath"];
  createDirectory: DirectoryPickerProps["createDirectory"];
  onCwdPanelClose: (value: string) => void;
  branch: string;
  /** The host list to offer, empty when the manifest has no non-local source. */
  hostOptions: readonly SpawnHostOption[];
  hostValue: string;
  hostLabel: string;
  connectingHosts: ReadonlySet<string>;
  onHostSelect: (id: string) => void;
  onHostConnect: (id: string) => void;
  onConnectHost: () => void;
  accessMode: string;
  accessOptions: MobilePickerOption[];
  /** The access the launch will actually use, resolved from the requested mode
   * and the Advanced sandbox override. */
  effectiveAccessLabel: string;
  /** The Advanced sandbox override is what sets the effective access. */
  advancedAccessOverride: boolean;
  onAccessChange: (value: string) => void;
  pluginPreview: PluginPreviewLoadState;
  pluginSelection: PluginSelectionState;
  /** The client a Details-opened plugin sheet reads component inventories through. */
  pluginClient: Pick<AppwireClientLike, "request">;
  /** The launch source the preview resolved against; the sheet's read follows it. */
  pluginHost?: string;
  pluginsSupported?: boolean;
  onPluginSelectionChange: (next: PluginSelectionState) => void;
  onPluginRetry: () => void;
}

type PickerName = "Host" | "Harness" | "Working directory" | "Access mode" | "Plugins";

const CLASS = {
  config: requireClass(styles.config, "MobileSettingRows.module.css", "config"),
  rowValue: requireClass(styles.rowValue, "MobileSettingRows.module.css", "rowValue"),
  sheetBody: requireClass(styles.sheetBody, "MobileSettingRows.module.css", "sheetBody"),
};

interface MobileSettingRowProps {
  label: string;
  value: string;
  onClick?: () => void;
  expanded?: boolean;
  disabled?: boolean;
  /** The state behind `value`, for a row whose value reads differently while
   * loading or failed (the plugin preview's status). */
  state?: string;
  /** The value is the working directory: wrap it in full instead of
   * truncating, so the final path component stays readable. */
  wrap?: boolean;
}

function MobileSettingRow({
  label,
  value,
  onClick,
  expanded = false,
  disabled = false,
  state,
  wrap = false,
}: MobileSettingRowProps) {
  const valueNode = (
    <span
      className={CLASS.rowValue}
      title={value}
      style={
        wrap ? { overflow: "visible", overflowWrap: "anywhere", textAlign: "start", whiteSpace: "normal" } : undefined
      }
    >
      {value}
    </span>
  );

  if (onClick === undefined || disabled) {
    return (
      <ListRow
        data-testid="mobile-spawn-row"
        data-label={label}
        data-state={state}
        label={label}
        value={valueNode}
        style={{ minHeight: "48px" }}
      />
    );
  }

  return (
    <ListRow
      data-testid="mobile-spawn-row"
      data-label={label}
      data-state={state}
      label={label}
      value={valueNode}
      style={{ minHeight: "48px" }}
      onSelect={onClick}
      aria-haspopup="dialog"
      aria-expanded={expanded}
      aria-label={`${label}: ${value}`}
    />
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
  const [draft, setDraft] = useState(value);

  useEffect(() => {
    if (open) setDraft(value);
  }, [open, value]);

  return (
    <Sheet open={open} side="bottom" onClose={onClose} title={`Choose ${name.toLowerCase()}`}>
      <div className={CLASS.sheetBody}>
        <RadioGroup
          variant="list"
          label={name}
          value={draft}
          options={options}
          // Arrows only preview; click, Enter and Space commit and close.
          onPreview={setDraft}
          onChange={(next) => {
            onChange(next);
            onClose();
          }}
        />
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
  hostOptions,
  hostValue,
  hostLabel,
  connectingHosts,
  onHostSelect,
  onHostConnect,
  onConnectHost,
  accessMode,
  accessOptions,
  effectiveAccessLabel,
  advancedAccessOverride,
  onAccessChange,
  pluginPreview,
  pluginSelection,
  pluginClient,
  pluginHost,
  pluginsSupported = true,
  onPluginSelectionChange,
  onPluginRetry,
}: MobileSettingRowsProps) {
  const [openPicker, setOpenPicker] = useState<PickerName | null>(null);
  const [pluginDraft, setPluginDraft] = useState<PluginSelectionState>(pluginSelection);
  const lastTriggerRef = useRef<HTMLButtonElement | null>(null);
  // A Connect that turns pending disables the button that held the keyboard,
  // and the browser then drops focus to <body> while the sheet stays open.
  // Keep the keyboard inside the sheet's host list.
  const hostSheetBodyRef = useRef<HTMLDivElement | null>(null);
  useFocusRehome(hostSheetBodyRef);

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
  // The row names the access the launch resolves to, not just the requested
  // mode, and says when Advanced options is what decides it. The sheet below
  // keeps the requested mode so it can still be changed there.
  const accessLabel = advancedAccessOverride ? `${effectiveAccessLabel} (Advanced)` : effectiveAccessLabel;
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
  const hasHostPicker = hostOptions.length > 0;

  return (
    <>
      <div className={CLASS.config} data-testid="mobile-spawn-config">
        {hasHostPicker && (
          <MobileSettingRow
            label="Host"
            value={hostLabel}
            onClick={() => open("Host")}
            expanded={openPicker === "Host"}
          />
        )}
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
          wrap
        />
        <MobileSettingRow label="Branch" value={branch === "" ? "No branch detected" : branch} />
        <MobileSettingRow
          label="Access mode"
          value={accessLabel}
          state={advancedAccessOverride ? "advanced" : undefined}
          onClick={() => open("Access mode")}
          expanded={openPicker === "Access mode"}
        />
        {pluginsSupported && (
          <MobileSettingRow
            label="Plugins"
            value={pluginSummary}
            state={pluginPreview.status}
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
        <InlineNotice
          tone="danger"
          live="status"
          action={
            <Button variant="quiet" size="sm" type="button" onClick={onPluginRetry}>
              Retry
            </Button>
          }
        >
          Couldn't inspect plugins
        </InlineNotice>
      )}

      {hasHostPicker && (
        <Sheet open={openPicker === "Host"} side="bottom" onClose={closePicker} title="Host">
          <div className={CLASS.sheetBody} ref={hostSheetBodyRef}>
            <SpawnHostSelection
              options={hostOptions}
              value={hostValue}
              connecting={connectingHosts}
              onSelect={(id) => {
                onHostSelect(id);
                closePicker();
              }}
              onConnect={onHostConnect}
              onConnectHost={() => {
                onConnectHost();
                closePicker();
              }}
            />
          </div>
        </Sheet>
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
        name="Access mode"
        value={accessMode}
        options={accessOptions}
        open={openPicker === "Access mode"}
        onClose={closePicker}
        onChange={onAccessChange}
      />

      {openPicker === "Working directory" && (
        <DirectoryPicker
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
            client={pluginClient}
            host={pluginHost}
            removeOnly={pluginPreview.status === "error"}
            onSelectionChange={setPluginDraft}
            onRetry={onPluginRetry}
          />
        </div>
      </Sheet>
    </>
  );
}
