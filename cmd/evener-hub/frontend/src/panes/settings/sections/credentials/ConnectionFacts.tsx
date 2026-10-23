import type { ReactNode } from "react";
import { Disclosure, Input, Select } from "../../../../widgets";
import styles from "./ConnectionFacts.module.css";
import { PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";

/** One label/value row of a connection card: a fact, the control or value in
 * its value cell, and the one local action the row offers. `htmlFor` names the
 * row's control when the label is a real field label; without it the label is
 * a plain fact label. */
export interface ConnectionFact {
  key: string;
  label: string;
  htmlFor?: string;
  value: ReactNode;
  help?: ReactNode;
  /** A refusal or state that follows the help line, as the card's rows place
   * it: value, then help, then the notice. */
  notice?: ReactNode;
  actions?: ReactNode;
}

/** The connection facts grid. The provider card and the settings sheet render
 * this same grid, so a connection reads the same in both places. */
export function ConnectionFacts({ rows, "data-testid": testId }: { rows: ConnectionFact[]; "data-testid"?: string }) {
  return (
    <dl className={styles.rows} data-testid={testId}>
      {rows.map((row) => {
        const helpId = row.htmlFor === undefined ? undefined : `${row.htmlFor}-help`;
        return (
          <div className={styles.row} key={row.key}>
            <dt className={styles.rowLabel}>
              {row.htmlFor !== undefined ? <label htmlFor={row.htmlFor}>{row.label}</label> : row.label}
            </dt>
            <dd className={styles.rowValue}>
              {row.value}
              {row.help !== undefined && (
                <span className={styles.rowHelp} id={helpId}>
                  {row.help}
                </span>
              )}
              {row.notice}
            </dd>
            {row.actions !== undefined && <dd className={styles.rowActions}>{row.actions}</dd>}
          </div>
        );
      })}
    </dl>
  );
}

/** One field of the full connection editor. */
interface ConnectionSetting {
  key: string;
  label: string;
  htmlFor: string;
  control: ReactNode;
  help?: ReactNode;
}

/** The five fields of the full connection editor, built once so the card and
 * the settings sheet cannot drift into different controls or ids. */
export function connectionSettingsFields({
  variableIdPrefix,
  fieldIdPrefix,
  variableRows,
  values,
  disabled,
  copy,
  onVariableChange,
  onFieldChange,
}: {
  variableIdPrefix: string;
  fieldIdPrefix: string;
  variableRows: { key: string; label: string }[];
  values: {
    vars: Record<string, string>;
    protocol: string;
    surface: string;
    apiKeyEnv: string;
    credentialHeader: string;
  };
  disabled: boolean;
  /** The copy this surface uses for the two fields whose names are not the
   * card's: the settings sheet says "API key environment variable" and keeps
   * its own credential-header hint. */
  copy: {
    apiKeyEnvLabel: string;
    credentialHeaderPlaceholder: string;
    credentialHeaderHelp?: ReactNode;
  };
  onVariableChange(key: string, value: string): void;
  onFieldChange(field: "protocol" | "surface" | "apiKeyEnv" | "credentialHeader", value: string): void;
}): ConnectionSetting[] {
  const { vars, protocol, surface, apiKeyEnv, credentialHeader } = values;
  const { apiKeyEnvLabel, credentialHeaderPlaceholder, credentialHeaderHelp } = copy;
  return [
    ...variableRows.map(({ key, label }) => {
      const id = `${variableIdPrefix}-var-${key}`;
      return {
        key,
        label,
        htmlFor: id,
        control: (
          <Input
            id={id}
            value={vars[key] ?? ""}
            disabled={disabled}
            onChange={(event) => onVariableChange(key, event.target.value)}
          />
        ),
      };
    }),
    {
      key: "protocol",
      label: "Protocol",
      htmlFor: `${fieldIdPrefix}-protocol`,
      control: (
        <Select
          id={`${fieldIdPrefix}-protocol`}
          value={protocol}
          onChange={(event) => onFieldChange("protocol", event.target.value)}
          options={PROTOCOL_OPTIONS}
          disabled={disabled}
        />
      ),
    },
    {
      key: "surface",
      label: "Surface",
      htmlFor: `${fieldIdPrefix}-surface`,
      control: (
        <Select
          id={`${fieldIdPrefix}-surface`}
          value={surface}
          onChange={(event) => onFieldChange("surface", event.target.value)}
          options={SURFACE_OPTIONS}
          disabled={disabled}
        />
      ),
    },
    {
      key: "apiKeyEnv",
      label: apiKeyEnvLabel,
      htmlFor: `${fieldIdPrefix}-apikeyenv`,
      control: (
        <Input
          id={`${fieldIdPrefix}-apikeyenv`}
          placeholder="e.g. PORTKEY_KEY"
          value={apiKeyEnv}
          disabled={disabled}
          onChange={(event) => onFieldChange("apiKeyEnv", event.target.value)}
        />
      ),
    },
    {
      key: "credentialHeader",
      label: "Credential header",
      htmlFor: `${fieldIdPrefix}-credentialheader`,
      control: (
        <Input
          id={`${fieldIdPrefix}-credentialheader`}
          placeholder={credentialHeaderPlaceholder}
          value={credentialHeader}
          disabled={disabled}
          onChange={(event) => onFieldChange("credentialHeader", event.target.value)}
        />
      ),
      help: credentialHeaderHelp,
    },
  ];
}

/** The "All connection settings" disclosure. The card opens it on its own
 * saved connection; the settings sheet keeps it open, because there it is the
 * sheet's form rather than a second editor behind a disclosure. */
export function ConnectionSettingsDisclosure({
  open,
  onOpenChange,
  fields,
  actions,
  "data-testid": testId,
}: {
  open: boolean;
  onOpenChange(open: boolean): void;
  fields: ConnectionSetting[];
  actions?: ReactNode;
  "data-testid"?: string;
}) {
  return (
    <Disclosure open={open} onOpenChange={onOpenChange} summary="All connection settings">
      <div className={styles.settings} data-settings-editor data-testid={testId}>
        {fields.map((field) => (
          <div className={styles.settingsRow} key={field.key}>
            <label className={styles.rowLabel} htmlFor={field.htmlFor}>
              {field.label}
            </label>
            <span className={styles.settingField}>{field.control}</span>
            {field.help !== undefined && (
              <span className={`${styles.rowHelp} ${styles.settingHelp}`} id={`${field.htmlFor}-help`}>
                {field.help}
              </span>
            )}
          </div>
        ))}
        {actions !== undefined && <div className={styles.settingsActions}>{actions}</div>}
      </div>
    </Disclosure>
  );
}
