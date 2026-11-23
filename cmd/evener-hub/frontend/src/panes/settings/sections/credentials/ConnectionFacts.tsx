import type { ReactNode } from "react";
import { Disclosure, Input, PropertyList, type PropertyListRow, Select } from "../../../../widgets";
import styles from "./ConnectionFacts.module.css";
import { PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";

/** One label/value row of a connection card: a fact, the control or value in
 * its value cell, and the one local action the row offers. */
export type ConnectionFact = PropertyListRow;

/** The connection facts grid. The provider card and the settings sheet render
 * this same grid, so a connection reads the same in both places. */
export function ConnectionFacts({ rows, "data-testid": testId }: { rows: ConnectionFact[]; "data-testid"?: string }) {
  return (
    <div data-testid={testId}>
      <PropertyList eyebrowLabels rows={rows.map((row) => ({ ...row, wrap: true }))} />
    </div>
  );
}

/** The fields of the full connection editor, built once so the card and
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
    codexClientVersion?: string;
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
  onFieldChange(
    field: "protocol" | "surface" | "apiKeyEnv" | "credentialHeader" | "codexClientVersion",
    value: string,
  ): void;
}): ConnectionFact[] {
  const { vars, protocol, surface, apiKeyEnv, credentialHeader, codexClientVersion } = values;
  const { apiKeyEnvLabel, credentialHeaderPlaceholder, credentialHeaderHelp } = copy;
  return [
    ...variableRows.map(({ key, label }) => {
      const id = `${idPrefix}-var-${key}`;
      return {
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
    ...(codexClientVersion === undefined
      ? []
      : [
          {
            label: "Codex client version",
            htmlFor: `${idPrefix}-codex-client-version`,
            value: (
              <Input
                id={`${idPrefix}-codex-client-version`}
                value={codexClientVersion}
                disabled={disabled}
                placeholder="Registry default"
                onChange={(event) => onFieldChange("codexClientVersion", event.target.value)}
              />
            ),
            help: "Used to discover Codex models. Leave blank to inherit the default.",
          },
        ]),
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
      <div data-settings-editor data-testid={testId}>
        <PropertyList layout="settings" rows={fields} />
        {actions !== undefined && <div className={styles.settingsActions}>{actions}</div>}
      </div>
    </Disclosure>
  );
}
