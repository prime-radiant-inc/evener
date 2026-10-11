import type { NavigationSessionSummary } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { type ChangeEvent, useEffect, useId, useState } from "react";
import { selectPinSectionSummaries } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";
import { Button, Dialog, FormRow, Input, Sheet, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useIsMobile } from "../useIsMobile";
import { isPinSectionNotFound, type PinSectionSummary } from "./actions";
import styles from "./railDialog.module.css";

export interface PinSectionPickerProps {
  session: NavigationSessionSummary;
  onAssign: (target: { section_id: string } | { section_name: string }, section?: PinSectionSummary) => Promise<void>;
  onClose: () => void;
}

const CLASS = {
  pickerList: requireClass(styles.pickerList, "railDialog.module.css", "pickerList"),
  pickerItem: requireClass(styles.pickerItem, "railDialog.module.css", "pickerItem"),
  pickerError: requireClass(styles.pickerError, "railDialog.module.css", "pickerError"),
};

function compareSections(a: PinSectionSummary, b: PinSectionSummary): number {
  return a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id);
}

function sortedPinSectionSummaries(): PinSectionSummary[] {
  return selectPinSectionSummaries(navigationStore.getState()).sort(compareSections);
}

export function PinSectionPicker({ session, onAssign, onClose }: PinSectionPickerProps) {
  const inputID = useId();
  const isMobile = useIsMobile();
  const [sections, setSections] = useState<PinSectionSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [nameMode, setNameMode] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let active = true;
    void navigationStore
      .getState()
      .loadPinCatalogPages()
      .then(() => {
        if (!active) return;
        setSections(sortedPinSectionSummaries());
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
      if (isPinSectionNotFound(err)) {
        try {
          await navigationStore.getState().loadPinCatalogPages(true);
          setSections(sortedPinSectionSummaries());
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
    <>
      <Button variant="quiet" onClick={() => setNameMode(false)} disabled={submitting}>
        Back
      </Button>
      <Button onClick={() => void assignNew()} disabled={submitting}>
        Create and pin
      </Button>
    </>
  ) : (
    <Button variant="quiet" onClick={onClose} disabled={submitting}>
      Cancel
    </Button>
  );

  const title = `Pin ${session.title}`;
  const body = (
    <>
      {nameMode ? (
        <FormRow label="Section name" htmlFor={inputID} error={error || undefined}>
          <Input
            id={inputID}
            value={name}
            onChange={(event: ChangeEvent<HTMLInputElement>) => {
              setName(event.target.value);
              setError("");
            }}
            disabled={submitting}
            aria-describedby={error ? `${inputID}-error` : undefined}
          />
        </FormRow>
      ) : (
        <>
          {loading && <Skeleton lines={2} />}
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
      {!nameMode && error && (
        <p className={CLASS.pickerError} role="alert">
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
