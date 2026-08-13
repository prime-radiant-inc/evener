// Structure adapted from Beautiful UI's Fine-tune inspector
// (https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane
// Levine — see LICENSES/beautiful-ui.txt. Values and markup translated
// into serf's CSS-module + token system; nothing is copy-pasted.
import { requireClass } from "../internal/requireClass";
import { Select } from "../select";
import styles from "./inspectorcard.module.css";

export interface InspectorCardProperty {
  key: string;
  label: string;
  value: string;
  /** When given (with onChange), the row renders as a Select instead of a
   * read-only value. */
  options?: string[];
  onChange?: (value: string) => void;
}

export interface InspectorCardProps {
  title: string;
  properties: InspectorCardProperty[];
}

const CLASS = {
  card: requireClass(styles.card, "inspectorcard.module.css", "card"),
  header: requireClass(styles.header, "inspectorcard.module.css", "header"),
  body: requireClass(styles.body, "inspectorcard.module.css", "body"),
  row: requireClass(styles.row, "inspectorcard.module.css", "row"),
  label: requireClass(styles.label, "inspectorcard.module.css", "label"),
  value: requireClass(styles.value, "inspectorcard.module.css", "value"),
};

function Row({ property }: { property: InspectorCardProperty }) {
  const { label, value, options, onChange } = property;
  const editable = options !== undefined && onChange !== undefined;

  return (
    <div className={CLASS.row} data-testid="inspector-row">
      <span className={CLASS.label}>{label}</span>
      {editable ? (
        <Select
          value={value}
          onChange={(event) => onChange(event.target.value)}
          options={options.map((option) => ({ value: option, label: option }))}
        />
      ) : (
        <span className={CLASS.value}>{value}</span>
      )}
    </div>
  );
}

/**
 * A property-inspector card: a micro-label header band over a list of
 * label/value rows, each hairline-separated. A row with `options` +
 * `onChange` composes the Select widget; otherwise it's a read-only mono
 * value.
 */
export function InspectorCard({ title, properties }: InspectorCardProps) {
  return (
    <div className={CLASS.card}>
      <div className={CLASS.header}>{title}</div>
      <div className={CLASS.body}>
        {properties.map((property) => (
          <Row key={property.key} property={property} />
        ))}
      </div>
    </div>
  );
}
