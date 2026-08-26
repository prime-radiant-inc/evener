// RootShell-owned concept switcher modal.
//
// Renders a single accessible dialog with three concept choices. When open,
// the background is marked inert and focus is trapped inside the dialog. On
// close (Escape, Back button, or overlay), the opener element receives focus
// restoration. Selecting a concept dispatches switchConcept then closes the
// dialog. Each choice is labeled with text, so the selected concept is
// announced without color-only meaning.

import { type JSX, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { ConceptId } from "./model";
import { Icon } from "./shared/Icon";
import "./ConceptSwitcher.css";

export interface ConceptChoice {
  readonly id: ConceptId;
  readonly label: string;
  readonly description: string;
}

export const CONCEPT_CHOICES: readonly ConceptChoice[] = [
  {
    id: "stillwater",
    label: "Stillwater",
    description: "A calm, steady conversation surface.",
  },
  {
    id: "constellation",
    label: "Constellation",
    description: "A connected, star-mapped view of sessions.",
  },
  {
    id: "field-notes",
    label: "Field Notes",
    description: "A lightweight, document-style surface.",
  },
];

export interface ConceptSwitcherProps {
  readonly open: boolean;
  readonly selectedConcept: ConceptId;
  readonly onSelect: (concept: ConceptId) => void;
  readonly onClose: () => void;
  /**
   * The element that had focus before the switcher opened. Focus is restored
   * here when the dialog closes. If not provided, the document body is used.
   */
  readonly openerRef?: React.RefObject<HTMLElement | null>;
}

export function ConceptSwitcher({
  open,
  selectedConcept,
  onSelect,
  onClose,
  openerRef,
}: ConceptSwitcherProps): JSX.Element | null {
  const dialogRef = useRef<HTMLDivElement>(null);
  const [wasOpen, setWasOpen] = useState(false);

  // Move focus into the dialog when it opens.
  useEffect(() => {
    if (!open) return;
    setWasOpen(true);
    const dialog = dialogRef.current;
    if (dialog === null) return;
    const id = window.requestAnimationFrame(() => {
      const first = dialog.querySelector<HTMLElement>("[data-concept-choice]");
      first?.focus();
    });
    return () => window.cancelAnimationFrame(id);
  }, [open]);

  // Restore focus to the opener when the dialog closes.
  useEffect(() => {
    if (!wasOpen || open) return;
    const target = openerRef?.current ?? null;
    if (target !== null) {
      target.focus();
    } else {
      document.body.focus();
    }
    setWasOpen(false);
  }, [open, wasOpen, openerRef]);

  // Escape closes the dialog without selecting.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, onClose]);

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "Tab") {
        trapTab(event, dialogRef.current);
      }
    },
    [],
  );

  const handleSelect = useCallback(
    (concept: ConceptId) => {
      onSelect(concept);
      onClose();
    },
    [onSelect, onClose],
  );

  if (!open) return null;

  const choices = CONCEPT_CHOICES;

  return createPortal(
    <div className="live-concept-switcher">
      <div
        className="live-concept-switcher__overlay"
        onClick={onClose}
        aria-hidden="true"
      />
      <div
        ref={dialogRef}
        className="live-concept-switcher__dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Switch concept"
        onKeyDown={handleKeyDown}
      >
        <div className="live-concept-switcher__header">
          <span className="live-concept-switcher__title">Switch concept</span>
          <button
            type="button"
            className="live-concept-switcher__back"
            onClick={onClose}
            aria-label="Back"
          >
            <Icon name="back" decorative />
            Back
          </button>
        </div>
        <div className="live-concept-switcher__choices">
          {choices.map((choice) => {
            const isSelected = choice.id === selectedConcept;
            return (
              <button
                key={choice.id}
                type="button"
                className="live-concept-switcher__choice"
                data-concept-choice={choice.id}
                data-concept-selected={isSelected ? "true" : "false"}
                aria-pressed={isSelected}
                onClick={() => handleSelect(choice.id)}
              >
                <span className="live-concept-switcher__choice-label">
                  {choice.label}
                </span>
                <span className="live-concept-switcher__choice-description">
                  {choice.description}
                </span>
                {isSelected ? (
                  <span className="live-concept-switcher__choice-selected">
                    Selected
                  </span>
                ) : null}
              </button>
            );
          })}
        </div>
      </div>
    </div>,
    document.body,
  );
}

function trapTab(
  event: React.KeyboardEvent<HTMLDivElement>,
  dialog: HTMLElement | null,
): void {
  if (dialog === null) return;
  const focusable = [
    ...dialog.querySelectorAll<HTMLElement>(
      '[data-concept-choice], button, [href], [tabindex]:not([tabindex="-1"])',
    ),
  ];
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (first === undefined || last === undefined) return;
  const active = document.activeElement as HTMLElement | null;
  if (event.shiftKey) {
    if (active === first || active === null) {
      event.preventDefault();
      last.focus();
    }
  } else {
    if (active === last) {
      event.preventDefault();
      first.focus();
    }
  }
}
