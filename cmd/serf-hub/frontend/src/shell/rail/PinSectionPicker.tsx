import { type ChangeEvent, useEffect, useId, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { PinSectionSummary, TreeNode } from "../../stores/tree";
import { Button, Dialog, Input } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { listPinSections } from "./actions";
import styles from "./Rail.module.css";

export interface PinSectionPickerProps {
  session: TreeNode;
  currentSectionId?: string;
  mode: "pin" | "move";
  onAssign: (target: { section_id: string } | { section_name: string }, section?: PinSectionSummary) => Promise<void>;
  onUnpin?: () => Promise<void>;
  onClose: () => void;
}

const CLASS = {
  pickerList: requireClass(styles.pickerList, "Rail.module.css", "pickerList"),
  pickerItem: requireClass(styles.pickerItem, "Rail.module.css", "pickerItem"),
  pickerCurrent: requireClass(styles.pickerCurrent, "Rail.module.css", "pickerCurrent"),
  pickerError: requireClass(styles.pickerError, "Rail.module.css", "pickerError"),
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
};

function compareSections(a: PinSectionSummary, b: PinSectionSummary): number {
  return a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id);
}

export function PinSectionPicker({
  session,
  currentSectionId,
  mode,
  onAssign,
  onUnpin,
  onClose,
}: PinSectionPickerProps) {
  const inputID = useId();
  const errorID = useId();
  const [sections, setSections] = useState<PinSectionSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [nameMode, setNameMode] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let active = true;
    void listPinSections()
      .then((summaries) => {
        if (active) setSections([...summaries].sort(compareSections));
      })
      .catch((err) => {
        if (active) setError(errorText(err));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  async function assignExisting(section: PinSectionSummary): Promise<void> {
    setError("");
    setSubmitting(true);
    try {
      await onAssign({ section_id: section.id }, section);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function assignNew(): Promise<void> {
    const normalized = name.trim();
    const count = Array.from(normalized).length;
    if (count === 0) {
      setError("Section name is required");
      return;
    }
    if (count > 80) {
      setError("Section names must be 80 characters or fewer");
      return;
    }
    setError("");
    setSubmitting(true);
    try {
      await onAssign({ section_name: normalized });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function unpin(): Promise<void> {
    if (!onUnpin) return;
    setError("");
    setSubmitting(true);
    try {
      await onUnpin();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setSubmitting(false);
    }
  }

  const footer = nameMode ? (
    <div className={CLASS.dialogActions}>
      <Button variant="quiet" onClick={() => setNameMode(false)} disabled={submitting}>
        Back
      </Button>
      <Button onClick={() => void assignNew()} disabled={submitting}>
        Create and pin
      </Button>
    </div>
  ) : (
    <div className={CLASS.dialogActions}>
      {mode === "move" && onUnpin && (
        <Button variant="dangerQuiet" onClick={() => void unpin()} disabled={submitting}>
          Unpin
        </Button>
      )}
      <Button variant="quiet" onClick={onClose} disabled={submitting}>
        Cancel
      </Button>
    </div>
  );

  return (
    <Dialog open onClose={onClose} title={`${mode === "move" ? "Move" : "Pin"} ${session.title}`} footer={footer}>
      {nameMode ? (
        <label className={CLASS.dialogField} htmlFor={inputID}>
          Section name
          <Input
            id={inputID}
            value={name}
            onChange={(event: ChangeEvent<HTMLInputElement>) => {
              setName(event.target.value);
              setError("");
            }}
            disabled={submitting}
            aria-describedby={error ? errorID : undefined}
          />
        </label>
      ) : (
        <>
          {loading && <p role="status">Loading sections…</p>}
          {!loading && (
            <ul className={CLASS.pickerList} aria-label="Pin sections">
              {sections.map((section) => {
                const current = section.id === currentSectionId;
                return (
                  <li key={section.id}>
                    <button
                      type="button"
                      className={CLASS.pickerItem}
                      aria-current={current ? "true" : undefined}
                      disabled={current || submitting}
                      onClick={() => void assignExisting(section)}
                    >
                      <span>{section.name}</span>
                      {current && <span className={CLASS.pickerCurrent}>✓ Current section</span>}
                    </button>
                  </li>
                );
              })}
              <li>
                <button
                  type="button"
                  className={CLASS.pickerItem}
                  onClick={() => setNameMode(true)}
                  disabled={submitting}
                >
                  New section…
                </button>
              </li>
            </ul>
          )}
        </>
      )}
      {error && (
        <p id={errorID} className={CLASS.pickerError} role="alert">
          {error}
        </p>
      )}
    </Dialog>
  );
}
