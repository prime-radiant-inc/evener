import { type ChangeEvent, useEffect, useId, useState } from "react";
import { errorText } from "../../protocol/errors";
import type { PinSectionSummary, TreeNode } from "../../stores/tree";
import { Button, Dialog, Input, Sheet } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useIsMobile } from "../useIsMobile";
import { isRailRequestStatus, listPinSections } from "./actions";
import styles from "./Rail.module.css";

export interface PinSectionPickerProps {
  session: TreeNode;
  onAssign: (target: { section_id: string } | { section_name: string }, section?: PinSectionSummary) => Promise<void>;
  onClose: () => void;
}

const CLASS = {
  pickerList: requireClass(styles.pickerList, "Rail.module.css", "pickerList"),
  pickerItem: requireClass(styles.pickerItem, "Rail.module.css", "pickerItem"),
  pickerError: requireClass(styles.pickerError, "Rail.module.css", "pickerError"),
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
};

function compareSections(a: PinSectionSummary, b: PinSectionSummary): number {
  return a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id);
}

export function PinSectionPicker({ session, onAssign, onClose }: PinSectionPickerProps) {
  const inputID = useId();
  const errorID = useId();
  const isMobile = useIsMobile();
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
      if (isRailRequestStatus(err, 404)) {
        try {
          const summaries = await listPinSections();
          setSections([...summaries].sort(compareSections));
        } catch {
          // Keep the assignment's useful not-found error visible. A later
          // picker mount will retry the summary request normally.
        }
      }
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
      <Button variant="quiet" onClick={onClose} disabled={submitting}>
        Cancel
      </Button>
    </div>
  );

  const title = `Pin ${session.title}`;
  const body = (
    <>
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
              {sections.map((section) => (
                <li key={section.id}>
                  <button
                    type="button"
                    className={CLASS.pickerItem}
                    disabled={submitting}
                    onClick={() => void assignExisting(section)}
                  >
                    <span>{section.name}</span>
                  </button>
                </li>
              ))}
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
    </>
  );

  // Mobile gets the standard Sheet (side=bottom, the same geometry the
  // session-tree drawer already uses - thumb-reachable header and close);
  // desktop keeps the centered Dialog. Both are the same OverlayPanel
  // contract (scrim, Escape, focus trap/restore), so only the panel's own
  // geometry differs.
  if (isMobile) {
    return (
      <Sheet side="bottom" open onClose={onClose} title={title} footer={footer}>
        {body}
      </Sheet>
    );
  }
  return (
    <Dialog open onClose={onClose} title={title} footer={footer}>
      {body}
    </Dialog>
  );
}
