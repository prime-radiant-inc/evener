import { useEffect, useId, useRef, useState } from "react";
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
import {
  Disclosure,
  FormRow,
  SegmentedControl,
  type SegmentedControlOption,
  Select,
  type SelectOption,
  Switch,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
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
  advancedPanel: requireClass(styles.advancedPanel, "transcriptDisplay.module.css", "advancedPanel"),
  fieldsets: requireClass(styles.fieldsets, "transcriptDisplay.module.css", "fieldsets"),
  fieldset: requireClass(styles.fieldset, "transcriptDisplay.module.css", "fieldset"),
  controls: requireClass(styles.controls, "transcriptDisplay.module.css", "controls"),
};

type ContentChoice = ContentLevel | "custom";

const LEVEL_OPTIONS: SegmentedControlOption<ContentChoice>[] = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
  { value: "activity", label: "Activity" },
  { value: "full", label: "Full", accessibleLabel: "Full detail" },
  { value: "custom", label: "Custom" },
];

const HOOK_EXIT_OPTIONS: SelectOption[] = [
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
  const hookExitId = useId();
  const lastCustom = useRef<ContentVector | undefined>(undefined);
  const config = normalizeConfig(value);
  useEffect(() => {
    if (config.content.kind === "custom") lastCustom.current = contentVector(config.content);
  }, [config.content]);
  const vector = contentVector(config.content);

  function emit(next: TranscriptDisplayConfigV1) {
    onChange(normalizeConfig(next));
  }

  function selectContent(choice: ContentChoice) {
    if (choice === "custom") {
      const customVector = lastCustom.current ?? contentVector(config.content);
      const next = { kind: "custom" as const, ...customVector };
      lastCustom.current = customVector;
      emit({ ...config, content: next });
      return;
    }
    emit({ ...config, content: { kind: "preset", level: choice } });
  }

  function updateContent(field: keyof ContentVector, checked: boolean) {
    const nextContent = {
      kind: "custom" as const,
      ...vector,
      [field]: checked,
    };
    lastCustom.current = nextContent;
    emit({ ...config, content: nextContent });
  }

  function updateAdvanced(field: keyof Omit<TranscriptDisplayAdvanced, "hookExits">, checked: boolean) {
    const nextAdvanced: TranscriptDisplayAdvanced = { ...config.advanced, [field]: checked };
    emit({ ...config, advanced: nextAdvanced });
  }

  function updateHookExits(next: string) {
    if (!isHookExitDetail(next)) return;
    emit({ ...config, advanced: { ...config.advanced, hookExits: next } });
  }

  const advancedCount = advancedEnabledCount(config);
  const disclosureSummary =
    config.content.kind === "custom"
      ? `Customize & advanced · Custom content · ${advancedCount} extras`
      : `Customize & advanced · ${advancedCount} extras`;
  const rootClassName = compact ? `${CLASS.root} ${CLASS.compact}` : CLASS.root;

  return (
    <section className={rootClassName} aria-label="Transcript detail editor">
      <SegmentedControl
        label="Transcript detail"
        value={config.content.kind === "preset" ? config.content.level : "custom"}
        options={LEVEL_OPTIONS}
        disabled={disabled}
        fullWidth
        onChange={selectContent}
      />
      <Disclosure open={advancedOpen} onOpenChange={setAdvancedOpen} disabled={disabled} summary={disclosureSummary}>
        <div className={CLASS.advancedPanel}>
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
                <FormRow label="Hook exit messages" htmlFor={hookExitId}>
                  <Select
                    id={hookExitId}
                    value={config.advanced.hookExits}
                    options={HOOK_EXIT_OPTIONS}
                    disabled={disabled}
                    onChange={(event) => updateHookExits(event.target.value)}
                  />
                </FormRow>
              </div>
            </fieldset>
          </div>
        </div>
      </Disclosure>
    </section>
  );
}
