// fields.tsx renders the non-collection LaunchOption kinds (Appendix B):
// text/multilineText/integer/select/radio/boolean/modelPicker/path, plus the
// 2 prompt-composite radios (systemPromptMode/systemPromptAppendMode) that
// fold 4 leaf wire fields into one control each. Collection kinds (pathList/
// modelList/envMap/mcpServerList) live in collectionFields.tsx instead.
//
// modelPicker renders the rich searchable ModelCatalog widget (wave 8 restore
// of the legacy settings-pickers.js Appendix A popup): the /api/models catalog
// with display names, capability badges, cost, and a Recent section. Wave 7
// shipped a plain free-text input as the interim; wave 8 swaps in the real
// widget, value/onChange unchanged so the schema/collect path is untouched.
//
// A browsable path kind (pathKind dir/file/outputFile) renders the PathField
// picker, which lists a directory's real contents over serf/paths/complete.
// A "command" pathKind is not a browsable path (it names an executable
// resolved off PATH, not a filesystem location the user points at), so it
// stays a plain input.
//
// One documented scope simplification from the legacy engine is not ported:
// the legacy engine's Constraint
// Validation API integration (validatePathInput/validateMCPCommandInput's
// setCustomValidity() + reportValidity() calls), which additionally pops
// the browser's own native validation-bubble UI alongside the custom
// inline error. This port's scalar path-kind fields (via
// LaunchConfigForm's own submit-time validate step) and collectionFields.
// tsx's add-time validation both surface ONLY the custom inline error -
// no native browser validation bubble.

import type { LaunchOption } from "../../../../protocol/types.gen";
import { extensionsStore } from "../../../../stores/extensions";
import type { LaunchConfigLayerName } from "../../../../stores/launchConfig";
import {
  FormRow,
  Input,
  ModelCatalog,
  PathField,
  type PathFieldKind,
  RadioGroup,
  type RadioGroupOption,
  Select,
  type SelectOption,
  Textarea,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";
import styles from "./fields.module.css";
import { emptyChoiceLabel, PROMPT_COMPOSITE_SPECS, resolvedEmptyChoice } from "./schema";

const CLASS = {
  defaultHint: requireClass(styles.defaultHint, "fields.module.css", "defaultHint"),
  radioBlock: requireClass(styles.radioBlock, "fields.module.css", "radioBlock"),
  radioHelp: requireClass(styles.radioHelp, "fields.module.css", "radioHelp"),
  compositeControls: requireClass(styles.compositeControls, "fields.module.css", "compositeControls"),
  modelBlock: requireClass(styles.modelBlock, "fields.module.css", "modelBlock"),
  modelLabel: requireClass(styles.modelLabel, "fields.module.css", "modelLabel"),
  modelHelp: requireClass(styles.modelHelp, "fields.module.css", "modelHelp"),
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

// The schema's pathKind vocabulary (cmd/serf-hub/internal/launchconfig/
// schema.go's LaunchPathKind) is wider than PathField's: "command" names an
// executable off PATH and "" names nothing browsable, so only the three
// filesystem kinds get a picker. undefined here means "render a plain input".
const BROWSABLE_PATH_KINDS: Record<string, PathFieldKind> = {
  dir: "dir",
  file: "file",
  outputFile: "outputFile",
};

function browsablePathKind(pathKind: string | undefined): PathFieldKind | undefined {
  return pathKind === undefined ? undefined : BROWSABLE_PATH_KINDS[pathKind];
}

/** The completion loader every path picker on this page shares. Imported off
 * the store the way the sibling modelPicker branch imports fetchModelCatalog
 * directly, rather than threading a prop down from LaunchConfigForm: launch
 * defaults are not scoped to a live session, so there is nothing per-caller to
 * inject. */
function completePaths(prefix: string, includeFiles: boolean): Promise<string[]> {
  return extensionsStore.getState().completePaths(prefix, includeFiles);
}

/** A path field: PathField in a FormRow, so the row's label, help, error, and
 * default hint all behave exactly as the plain-Input rows around it do. The
 * trigger is a `<button id>`, which a `<label htmlFor>` labels like any other
 * form control - unlike ModelCatalog, whose own inner combobox carries its
 * name and which therefore needs the label-as-span treatment. */
function PathPickerRow({
  fieldId,
  label,
  kind,
  value,
  onChange,
  help,
  error,
  placeholder,
  globalDefaultHint,
}: {
  fieldId: string;
  label: string;
  kind: PathFieldKind;
  value: string;
  onChange: (value: string) => void;
  help?: string;
  error?: string;
  placeholder: string;
  globalDefaultHint?: string;
}) {
  return (
    <FormRow label={label} htmlFor={fieldId} help={help} error={error}>
      <PathField
        id={fieldId}
        value={value}
        onChange={onChange}
        kind={kind}
        complete={completePaths}
        placeholder={placeholder}
      />
      <DefaultHint text={globalDefaultHint} />
    </FormRow>
  );
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

  if (option.kind === "modelPicker") {
    // A composite widget, not a single labelable control, so the field label
    // is a plain span (mirroring the spawn form's own Model field) rather than
    // a FormRow's <label htmlFor> - the ModelCatalog's inner combobox carries
    // its own accessible name. loadCatalog is the unscoped /api/models call
    // (launch defaults aren't harness/cwd-scoped the way a live spawn is).
    return (
      <div className={CLASS.modelBlock}>
        <span className={CLASS.modelLabel}>{option.label}</span>
        <ModelCatalog value={value} onChange={onChange} loadCatalog={() => fetchModelCatalog()} />
        {option.description && <p className={CLASS.modelHelp}>{option.description}</p>}
        <DefaultHint text={globalDefaultHint} />
      </div>
    );
  }

  if (option.kind === "path") {
    const pathKind = browsablePathKind(option.pathKind);
    if (pathKind) {
      return (
        <PathPickerRow
          fieldId={fieldId}
          label={option.label}
          kind={pathKind}
          value={value}
          onChange={onChange}
          help={option.description}
          error={error}
          placeholder={emptyChoiceLabel(layer)}
          globalDefaultHint={globalDefaultHint}
        />
      );
    }
  }

  // text, integer, and a non-browsable path (a command, or an unkinded one) -
  // all a plain (possibly numeric) input.
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
        {/* The same picker the scalar path kind renders: this sub-field's wire
            field IS systemPromptFile/systemPromptAppendFile, a file-kind path
            in the schema, so it must not be a text box in one place and a
            picker in another. */}
        <PathPickerRow
          fieldId={fileFieldId}
          label={spec.fileLabel}
          kind="file"
          value={fileValue}
          onChange={onFileChange}
          error={fileError}
          placeholder={emptyChoiceLabel(layer)}
          globalDefaultHint={fileGlobalDefaultHint}
        />
        <FormRow label={spec.textLabel} htmlFor={textFieldId}>
          <Textarea id={textFieldId} value={textValue} onChange={(e) => onTextChange(e.target.value)} autoGrow />
          <DefaultHint text={textGlobalDefaultHint} />
        </FormRow>
      </div>
    </div>
  );
}
