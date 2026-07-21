// fields.tsx renders the non-collection LaunchOption kinds (Appendix B):
// text/multilineText/integer/select/radio/boolean/modelPicker/path, plus the
// 2 prompt-composite radios (systemPromptMode/systemPromptAppendMode) that
// fold 4 leaf wire fields into one control each. Collection kinds (pathList/
// modelList/envMap/mcpServerList) live in collectionFields.tsx instead.
//
// Two deliberate, documented scope simplifications from the legacy engine
// (see this task's own report for the write-up):
//   - modelPicker renders as a plain free-text "provider/model" input, not
//     the searchable popup (settings-pickers.js Appendix A infrastructure -
//     a REST-backed widget outside this stream's manifest).
//   - path/pathList kinds never render PathPicker: the widget's own
//     listChildren contract only ever lists DIRECTORIES (serf/dirs/complete
//     filters to entry.IsDir()), so it cannot serve file/outputFile kind
//     scalars, and no scalar dir-kind field exists in the real schema
//     (cmd/serf-hub/internal/launchconfig/schema.go) - only pathList fields
//     (skillsDirs/pluginDirs) are dir-kind, and serf/dirs/complete is not
//     part of this task's assigned wire ground truth. Every path-kind field
//     renders as a validated free-text input instead (validation still
//     happens - see collectionFields.tsx and LaunchConfigForm's own
//     validate step).

import type { LaunchOption } from "../../../../protocol/types.gen";
import type { LaunchConfigLayerName } from "../../../../stores/launchConfig";
import {
  FormRow,
  Input,
  RadioGroup,
  type RadioGroupOption,
  Select,
  type SelectOption,
  Textarea,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./fields.module.css";
import { emptyChoiceLabel, PROMPT_COMPOSITE_SPECS, resolvedEmptyChoice } from "./schema";

const CLASS = {
  defaultHint: requireClass(styles.defaultHint, "fields.module.css", "defaultHint"),
  radioBlock: requireClass(styles.radioBlock, "fields.module.css", "radioBlock"),
  radioHelp: requireClass(styles.radioHelp, "fields.module.css", "radioHelp"),
  compositeControls: requireClass(styles.compositeControls, "fields.module.css", "compositeControls"),
};

function nonEmptyChoices(option: LaunchOption): RadioGroupOption[] {
  return (option.choices ?? [])
    .filter((c) => (c.value ?? "") !== "")
    .map((c) => ({ value: c.value, label: c.label || c.value, disabled: c.disabled }));
}

function DefaultHint({ text }: { text?: string }) {
  if (!text) return null;
  return <span className={CLASS.defaultHint}>{text}</span>;
}

export interface ScalarFieldProps {
  option: LaunchOption;
  layer: LaunchConfigLayerName;
  value: string;
  onChange: (value: string) => void;
  /** Precomputed "default: {value}" text (project layer only, truncated by
   * the caller) - undefined everywhere else. */
  globalDefaultHint?: string;
  /** Submit-time serf/path/validate failure message for a `path`-kind
   * field (LaunchConfigForm's own validate step) - shown in place of
   * `option.description`, matching FormRow's own error-takes-over-help
   * contract. Never set for any other kind. */
  error?: string;
}

/** Renders one non-collection LaunchOption per its `kind`. */
export function ScalarField({ option, layer, value, onChange, globalDefaultHint, error }: ScalarFieldProps) {
  const fieldId = `launch-field-${option.field}`;

  if (option.kind === "radio") {
    const options: RadioGroupOption[] = [resolvedEmptyChoice(option, layer), ...nonEmptyChoices(option)];
    return (
      <div className={CLASS.radioBlock}>
        <RadioGroup label={option.label} value={value} onChange={onChange} options={options} />
        <DefaultHint text={globalDefaultHint} />
        {option.description && <p className={CLASS.radioHelp}>{option.description}</p>}
      </div>
    );
  }

  if (option.kind === "select" || option.kind === "boolean") {
    const options: SelectOption[] =
      option.kind === "boolean"
        ? [
            { value: "", label: resolvedEmptyChoice(option, layer).label },
            { value: "true", label: "true" },
            { value: "false", label: "false" },
          ]
        : [resolvedEmptyChoice(option, layer), ...nonEmptyChoices(option)];
    return (
      <FormRow label={option.label} htmlFor={fieldId} help={option.description}>
        <Select id={fieldId} value={value} onChange={(e) => onChange(e.target.value)} options={options} />
        <DefaultHint text={globalDefaultHint} />
      </FormRow>
    );
  }

  if (option.kind === "multilineText") {
    return (
      <FormRow label={option.label} htmlFor={fieldId} help={option.description}>
        <Textarea id={fieldId} value={value} onChange={(e) => onChange(e.target.value)} autoGrow />
      </FormRow>
    );
  }

  // text, integer, modelPicker, path - all a plain (possibly numeric) input.
  // See this file's own top comment for why modelPicker/path don't get their
  // legacy-specific widgets here.
  return (
    <FormRow label={option.label} htmlFor={fieldId} help={option.description} error={error}>
      <Input
        id={fieldId}
        type={option.kind === "integer" ? "number" : "text"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={emptyChoiceLabel(layer)}
      />
      <DefaultHint text={globalDefaultHint} />
    </FormRow>
  );
}

export interface PromptCompositeFieldProps {
  /** The systemPromptMode or systemPromptAppendMode option itself. */
  option: LaunchOption;
  layer: LaunchConfigLayerName;
  modeValue: string;
  fileValue: string;
  textValue: string;
  onModeChange: (value: string) => void;
  onFileChange: (value: string) => void;
  onTextChange: (value: string) => void;
  fileGlobalDefaultHint?: string;
  textGlobalDefaultHint?: string;
  /** Submit-time serf/path/validate failure for the file sub-field, same
   * contract as ScalarField's own `error` prop. */
  fileError?: string;
}

/**
 * Renders systemPromptMode/systemPromptAppendMode as a mode radio group plus
 * its 2 leaf sub-fields (file path input, inline textarea), always both
 * visible and always both editable regardless of which mode is selected -
 * matching the legacy's own "type into the inactive one, it's silently never
 * validated/collected" behavior (schema.ts's inactivePromptDependent +
 * collectConfig are what actually gate save-time inclusion, not this
 * component). Deliberately simpler than the legacy's nested-control-inside-
 * the-radio-option layout: the 2 sub-fields render as their own rows below
 * the radio group instead of nested inside it - same information, same
 * always-editable behavior, less bespoke composite-widget code to
 * hand-roll outside the tested RadioGroup/FormRow primitives.
 */
export function PromptCompositeField({
  option,
  layer,
  modeValue,
  fileValue,
  textValue,
  onModeChange,
  onFileChange,
  onTextChange,
  fileGlobalDefaultHint,
  textGlobalDefaultHint,
  fileError,
}: PromptCompositeFieldProps) {
  const spec = PROMPT_COMPOSITE_SPECS[option.wireField];
  if (!spec) throw new Error(`PromptCompositeField: "${option.wireField}" is not a known prompt-composite wire field`);
  const modeOptions: RadioGroupOption[] = [resolvedEmptyChoice(option, layer), ...nonEmptyChoices(option)];
  const fileFieldId = `launch-field-${option.field}-file`;
  const textFieldId = `launch-field-${option.field}-text`;

  return (
    <div className={CLASS.radioBlock}>
      <RadioGroup label={option.label} value={modeValue} onChange={onModeChange} options={modeOptions} />
      {option.description && <p className={CLASS.radioHelp}>{option.description}</p>}
      <div className={CLASS.compositeControls}>
        <FormRow label={spec.fileLabel} htmlFor={fileFieldId} error={fileError}>
          <Input
            id={fileFieldId}
            value={fileValue}
            onChange={(e) => onFileChange(e.target.value)}
            placeholder={emptyChoiceLabel(layer)}
          />
          <DefaultHint text={fileGlobalDefaultHint} />
        </FormRow>
        <FormRow label={spec.textLabel} htmlFor={textFieldId}>
          <Textarea id={textFieldId} value={textValue} onChange={(e) => onTextChange(e.target.value)} autoGrow />
          <DefaultHint text={textGlobalDefaultHint} />
        </FormRow>
      </div>
    </div>
  );
}
