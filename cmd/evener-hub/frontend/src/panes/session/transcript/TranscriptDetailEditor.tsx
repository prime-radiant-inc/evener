import { useId, useState } from "react";
import {
  advancedEnabledCount,
  type ContentLevel,
  type ContentVector,
  type HookExitDetail,
  normalizeConfig,
  presetContent,
  type TranscriptDisplayAdvanced,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { requireClass } from "../../../widgets/internal/requireClass";
import { RadioGroup, type RadioGroupOption } from "../../../widgets/radiogroup";
import { Switch } from "../../../widgets/switch";
import styles from "./transcriptDisplay.module.css";

export interface TranscriptDetailEditorProps {
  value: TranscriptDisplayConfigV1;
  onChange(value: TranscriptDisplayConfigV1): void;
  disabled?: boolean;
  compact?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "transcriptDisplay.module.css", "root"),
  compact: requireClass(styles.compact, "transcriptDisplay.module.css", "compact"),
  track: requireClass(styles.track, "transcriptDisplay.module.css", "track"),
  readout: requireClass(styles.readout, "transcriptDisplay.module.css", "readout"),
  custom: requireClass(styles.custom, "transcriptDisplay.module.css", "custom"),
  advancedToggle: requireClass(styles.advancedToggle, "transcriptDisplay.module.css", "advancedToggle"),
  advancedPanel: requireClass(styles.advancedPanel, "transcriptDisplay.module.css", "advancedPanel"),
  fieldsets: requireClass(styles.fieldsets, "transcriptDisplay.module.css", "fieldsets"),
  fieldset: requireClass(styles.fieldset, "transcriptDisplay.module.css", "fieldset"),
  controls: requireClass(styles.controls, "transcriptDisplay.module.css", "controls"),
  selectLabel: requireClass(styles.selectLabel, "transcriptDisplay.module.css", "selectLabel"),
  select: requireClass(styles.select, "transcriptDisplay.module.css", "select"),
  critical: requireClass(styles.critical, "transcriptDisplay.module.css", "critical"),
};

const LEVEL_OPTIONS: RadioGroupOption[] = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
  { value: "activity", label: "Activity" },
  { value: "full", label: "Full", accessibleLabel: "Full detail" },
];

const HOOK_EXIT_OPTIONS: ReadonlyArray<{ value: HookExitDetail; label: string }> = [
  { value: "none", label: "None" },
  { value: "successful", label: "Successful" },
  { value: "all", label: "All" },
];

function contentVector(content: TranscriptDisplayConfigV1["content"]): ContentVector {
  return content.kind === "preset" ? presetContent(content.level) : { ...content };
}

function isHookExitDetail(value: string): value is HookExitDetail {
  return value === "none" || value === "successful" || value === "all";
}

export function TranscriptDetailEditor({
  value,
  onChange,
  disabled = false,
  compact = false,
}: TranscriptDetailEditorProps) {
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const advancedPanelId = useId();
  const config = normalizeConfig(value);
  const selectedLevel = config.content.kind === "preset" ? config.content.level : undefined;
  const vector = contentVector(config.content);

  function emit(next: TranscriptDisplayConfigV1) {
    onChange(normalizeConfig(next));
  }

  function selectLevel(level: string) {
    if (level === "chat" || level === "intent" || level === "tools" || level === "activity" || level === "full") {
      emit({ ...config, content: { kind: "preset", level: level as ContentLevel } });
    }
  }

  function updateContent(field: keyof ContentVector, checked: boolean) {
    const nextContent = {
      kind: "custom" as const,
      toolIntent: field === "toolIntent" ? checked : vector.toolIntent,
      toolCalls: field === "toolCalls" ? checked : vector.toolCalls,
      reasoning: field === "reasoning" ? checked : vector.reasoning,
      expandByDefault: field === "expandByDefault" ? checked : vector.expandByDefault,
    };
    emit({ ...config, content: nextContent });
  }

  function updateAdvanced(field: keyof Omit<TranscriptDisplayAdvanced, "hookExits">, checked: boolean) {
    const nextAdvanced: TranscriptDisplayAdvanced = {
      roundTimings: field === "roundTimings" ? checked : config.advanced.roundTimings,
      tokenCounts: field === "tokenCounts" ? checked : config.advanced.tokenCounts,
      estimatedCost: field === "estimatedCost" ? checked : config.advanced.estimatedCost,
      systemEvents: field === "systemEvents" ? checked : config.advanced.systemEvents,
      promptEvents: field === "promptEvents" ? checked : config.advanced.promptEvents,
      hookExits: config.advanced.hookExits,
    };
    emit({ ...config, advanced: nextAdvanced });
  }

  function updateHookExits(next: string) {
    if (!isHookExitDetail(next)) return;
    emit({ ...config, advanced: { ...config.advanced, hookExits: next } });
  }

  const readout =
    selectedLevel === undefined
      ? "Custom"
      : selectedLevel === "full"
        ? "Full detail"
        : LEVEL_OPTIONS.find((option) => option.value === selectedLevel)?.label;
  const advancedCount = advancedEnabledCount(config);
  const advancedSummary =
    selectedLevel === undefined ? `Custom content · ${advancedCount} extras` : `${advancedCount} enabled`;
  const rootClassName = compact ? `${CLASS.root} ${CLASS.compact}` : CLASS.root;

  return (
    <section className={rootClassName} aria-label="Transcript detail editor">
      <div className={CLASS.track}>
        <RadioGroup
          label="Transcript detail"
          value={selectedLevel ?? "custom"}
          options={LEVEL_OPTIONS}
          disabled={disabled}
          onChange={selectLevel}
        />
      </div>
      <p className={CLASS.readout}>
        Current detail: <strong className={selectedLevel === undefined ? CLASS.custom : undefined}>{readout}</strong>
      </p>

      <button
        type="button"
        className={CLASS.advancedToggle}
        aria-expanded={advancedOpen}
        aria-controls={advancedPanelId}
        disabled={disabled}
        onClick={() => setAdvancedOpen((open) => !open)}
      >
        <span>Advanced · {advancedSummary}</span>
        <span aria-hidden="true"> {advancedOpen ? "▴" : "▾"}</span>
      </button>

      {advancedOpen && (
        <div id={advancedPanelId} className={CLASS.advancedPanel}>
          <div className={CLASS.fieldsets}>
            <fieldset className={CLASS.fieldset}>
              <legend>Content</legend>
              <div className={CLASS.controls}>
                <Switch
                  label="Tool intent"
                  checked={vector.toolIntent}
                  disabled={disabled}
                  onChange={(checked) => updateContent("toolIntent", checked)}
                />
                <Switch
                  label="Tool calls"
                  checked={vector.toolCalls}
                  disabled={disabled}
                  onChange={(checked) => updateContent("toolCalls", checked)}
                />
                <Switch
                  label="Reasoning"
                  checked={vector.reasoning}
                  disabled={disabled}
                  onChange={(checked) => updateContent("reasoning", checked)}
                />
                <Switch
                  label="Expand visible details by default"
                  checked={vector.expandByDefault}
                  disabled={disabled}
                  onChange={(checked) => updateContent("expandByDefault", checked)}
                />
              </div>
            </fieldset>

            <fieldset className={CLASS.fieldset}>
              <legend>Metrics</legend>
              <div className={CLASS.controls}>
                <Switch
                  label="Round timings"
                  checked={config.advanced.roundTimings}
                  disabled={disabled}
                  onChange={(checked) => updateAdvanced("roundTimings", checked)}
                />
                <Switch
                  label="Token counts"
                  checked={config.advanced.tokenCounts}
                  disabled={disabled}
                  onChange={(checked) => updateAdvanced("tokenCounts", checked)}
                />
                <Switch
                  label="Estimated cost"
                  checked={config.advanced.estimatedCost}
                  disabled={disabled}
                  onChange={(checked) => updateAdvanced("estimatedCost", checked)}
                />
              </div>
            </fieldset>

            <fieldset className={CLASS.fieldset}>
              <legend>Diagnostics</legend>
              <div className={CLASS.controls}>
                <Switch
                  label="Low-level system events"
                  checked={config.advanced.systemEvents}
                  disabled={disabled}
                  onChange={(checked) => updateAdvanced("systemEvents", checked)}
                />
                <Switch
                  label="System prompt and prompt-loaded events"
                  checked={config.advanced.promptEvents}
                  disabled={disabled}
                  onChange={(checked) => updateAdvanced("promptEvents", checked)}
                />
                <label className={CLASS.selectLabel}>
                  Hook exit messages
                  <select
                    className={CLASS.select}
                    aria-label="Hook exit messages"
                    value={config.advanced.hookExits}
                    disabled={disabled}
                    onChange={(event) => updateHookExits(event.target.value)}
                  >
                    {HOOK_EXIT_OPTIONS.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            </fieldset>
          </div>
        </div>
      )}

      <p className={CLASS.critical}>
        Critical rows remain visible at every detail level: questions, requests, active work, steering, warnings,
        failures, interruptions, and recovery actions. These rows are locked explanatory content, not editor controls.
      </p>
    </section>
  );
}
