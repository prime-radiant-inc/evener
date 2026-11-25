import type { NavigationSessionSummary } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { selectPinSectionSummaries } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";
import { Button, Dialog, InlineNotice, ListRow, Sheet, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useIsMobile } from "../useIsMobile";
import { isPinSectionNotFound, type PinSectionSummary } from "./actions";
import { PinSectionNameField, validatePinSectionName } from "./pinSectionForm";
import styles from "./railDialog.module.css";

export interface PinSectionPickerProps {
  session: NavigationSessionSummary;
  onAssign: (target: { section_id: string } | { section_name: string }, section?: PinSectionSummary) => Promise<void>;
  onClose: () => void;
}

const CLASS = {
  pickerBody: requireClass(styles.pickerBody, "railDialog.module.css", "pickerBody"),
  pickerList: requireClass(styles.pickerList, "railDialog.module.css", "pickerList"),
};

export function comparePinSections(a: { id: string; name: string }, b: { id: string; name: string }): number {
  return a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id);
}

function sortedPinSectionSummaries(): PinSectionSummary[] {
  return selectPinSectionSummaries(navigationStore.getState()).sort(comparePinSections);
}

export function PinSectionPicker({ session, onAssign, onClose }: PinSectionPickerProps) {
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
        if (active) setError(friendlyErrorMessage(err));
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
      setError(friendlyErrorMessage(err));
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
    const invalid = validatePinSectionName(name);
    if (invalid !== undefined) {
      setError(invalid);
      return;
    }
    const normalized = name.trim();
    setError("");
    setSubmitting(true);
    try {
      await onAssign({ section_name: normalized });
    } catch (err) {
      setError(friendlyErrorMessage(err));
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
    <div className={CLASS.pickerBody}>
      {nameMode ? (
        <PinSectionNameField
          value={name}
          onChange={(value) => {
            setName(value);
            setError("");
          }}
          error={error || undefined}
          disabled={submitting}
        />
      ) : (
        <>
          {loading && <Skeleton lines={2} />}
          {!loading && (
            <ul className={CLASS.pickerList} aria-label="Pin sections">
              {sections.map((section) => (
                <li key={section.id}>
                  <ListRow
                    variant="plain"
                    label={section.name}
                    disabled={submitting}
                    onSelect={() => void assignExisting(section)}
                  />
                </li>
              ))}
              <li>
                <ListRow
                  variant="plain"
                  label="New section…"
                  disabled={submitting}
                  onSelect={() => setNameMode(true)}
                />
              </li>
            </ul>
          )}
        </>
      )}
      {!nameMode && error && <InlineNotice tone="danger">{error}</InlineNotice>}
    </div>
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
