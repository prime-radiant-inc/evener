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
   * it: value, then help, then the notice. Settings rows never set it. */
  notice?: ReactNode;
  actions?: ReactNode;
}

/** Matches FormRow's documented help-id scheme (widgets/formrow/index.tsx):
 * a field's help is `${htmlFor}-help`. */
function helpIdFor(htmlFor: string | undefined): string | undefined {
  return htmlFor === undefined ? undefined : `${htmlFor}-help`;
}

/** One row, in either the facts grid (label, value, and an optional action) or
 * the full editor's settings grid (label and control, help below). */
function ConnectionFactRow({ row, settings }: { row: ConnectionFact; settings: boolean }) {
  const helpId = helpIdFor(row.htmlFor);
  if (settings) {
    return (
      <div className={styles.settingsRow}>
        {row.htmlFor !== undefined ? (
          <label className={styles.rowLabel} htmlFor={row.htmlFor}>
            {row.label}
          </label>
        ) : (
          <span className={styles.rowLabel}>{row.label}</span>
        )}
        <span className={styles.settingField}>{row.value}</span>
        {row.help !== undefined && (
          <span className={`${styles.rowHelp} ${styles.settingHelp}`} id={helpId}>
            {row.help}
          </span>
        )}
      </div>
    );
  }
  return (
    <div className={styles.row}>
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
}

/** The connection facts grid. The provider card and the settings sheet render
 * this same grid, so a connection reads the same in both places. */
export function ConnectionFacts({ rows, "data-testid": testId }: { rows: ConnectionFact[]; "data-testid"?: string }) {
  return (
    <dl className={styles.rows} data-testid={testId}>
      {rows.map((row) => (
        <ConnectionFactRow key={row.key} row={row} settings={false} />
      ))}
    </dl>
  );
}

/** The five fields of the full connection editor, built once so the card and
 * the settings sheet cannot drift into different controls or ids. */
export function connectionSettingsFields({
  idPrefix,
  variableRows,
  values,
  disabled,
  copy,
  onVariableChange,
  onFieldChange,
}: {
  idPrefix: string;
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
}): ConnectionFact[] {
  const { vars, protocol, surface, apiKeyEnv, credentialHeader } = values;
  const { apiKeyEnvLabel, credentialHeaderPlaceholder, credentialHeaderHelp } = copy;
  return [
    ...variableRows.map(({ key, label }) => {
      const id = `${idPrefix}-var-${key}`;
      return {
        key,
        label,
        htmlFor: id,
        value: (
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
      htmlFor: `${idPrefix}-protocol`,
      value: (
        <Select
          id={`${idPrefix}-protocol`}
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
      htmlFor: `${idPrefix}-surface`,
      value: (
        <Select
          id={`${idPrefix}-surface`}
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
      htmlFor: `${idPrefix}-apikeyenv`,
      value: (
        <Input
          id={`${idPrefix}-apikeyenv`}
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
      htmlFor: `${idPrefix}-credentialheader`,
      value: (
        <Input
          id={`${idPrefix}-credentialheader`}
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
  fields: ConnectionFact[];
  actions?: ReactNode;
  "data-testid"?: string;
}) {
  return (
    <Disclosure open={open} onOpenChange={onOpenChange} summary="All connection settings">
      <div className={styles.settings} data-settings-editor data-testid={testId}>
        {fields.map((field) => (
          <ConnectionFactRow key={field.key} row={field} settings />
        ))}
        {actions !== undefined && <div className={styles.settingsActions}>{actions}</div>}
      </div>
    </Disclosure>
  );
}
