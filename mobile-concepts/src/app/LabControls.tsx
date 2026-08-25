import { type KeyboardEvent, useEffect, useRef } from "react";
import type { Appearance, ScenarioId, TextScale } from "../core/model";
import type { PlatformPrimitives } from "../core/platform";
import { scenarioIds } from "../core/scenarios";
import type { PrototypeAction } from "../core/state";
import { usePrototypeState } from "../core/store";

const appearances = [
  "system",
  "light",
  "dark",
] as const satisfies readonly Appearance[];
const textScales = [
  "standard",
  "large",
  "accessibility",
] as const satisfies readonly TextScale[];

export interface LabControlsProps {
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
}

function ScenarioControls({
  value,
  dispatch,
}: {
  value: ScenarioId;
  dispatch(action: PrototypeAction): void;
}) {
  return (
    <fieldset>
      <legend>Scenario</legend>
      <div className="lab-controls__options lab-controls__options--scenarios">
        {scenarioIds.map((scenario) => (
          <label key={scenario}>
            <input
              type="radio"
              name="scenario"
              checked={value === scenario}
              onChange={() => dispatch({ type: "setScenario", scenario })}
            />
            <span>{scenario}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

export function LabControls({ primitives, dispatch }: LabControlsProps) {
  const overlay = usePrototypeState((state) => state.overlay);
  const scenario = usePrototypeState((state) => state.scenario);
  const appearance = usePrototypeState((state) => state.appearance);
  const textScale = usePrototypeState((state) => state.textScale);
  const reducedMotion = usePrototypeState((state) => state.reducedMotion);
  const open = overlay === "lab-controls";
  const modalActive = overlay !== null;
  const openerRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const wasOpenRef = useRef(false);
  const resetRequestedRef = useRef(false);

  useEffect(() => {
    if (open) {
      if (!wasOpenRef.current && previousFocusRef.current === null) {
        previousFocusRef.current =
          document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null;
      }
      wasOpenRef.current = true;
      closeRef.current?.focus();
      return;
    }

    if (!wasOpenRef.current) return;
    wasOpenRef.current = false;
    const resetTarget = document.querySelector<HTMLElement>(
      "[data-gallery-focus-target='true']",
    );
    const priorTarget = previousFocusRef.current?.isConnected
      ? previousFocusRef.current
      : openerRef.current;
    const target = resetRequestedRef.current ? resetTarget : priorTarget;
    resetRequestedRef.current = false;
    target?.focus();
    previousFocusRef.current = null;
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const containFocus = (event: FocusEvent) => {
      if (
        event.target instanceof Node &&
        !dialogRef.current?.contains(event.target)
      ) {
        closeRef.current?.focus();
      }
    };
    document.addEventListener("focusin", containFocus);
    return () => document.removeEventListener("focusin", containFocus);
  }, [open]);

  const trapFocus = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key !== "Tab") return;
    const focusable = Array.from(
      event.currentTarget.querySelectorAll<HTMLElement>(
        "button:not([disabled]):not([tabindex='-1']), input:not([disabled]):not([tabindex='-1']), select:not([disabled]):not([tabindex='-1']), textarea:not([disabled]):not([tabindex='-1']), [href]:not([tabindex='-1']), [tabindex]:not([tabindex='-1'])",
      ),
    );
    const first = focusable.at(0);
    const last = focusable.at(-1);
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <>
      <button
        ref={openerRef}
        className="lab-controls-trigger"
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-hidden={modalActive || undefined}
        inert={modalActive || undefined}
        tabIndex={modalActive ? -1 : undefined}
        onClick={(event) => {
          previousFocusRef.current = event.currentTarget;
          dispatch({ type: "openOverlay", overlay: "lab-controls" });
        }}
      >
        Lab Controls
      </button>
      {open ? (
        <div className="lab-controls-backdrop">
          <section
            ref={dialogRef}
            className="lab-controls"
            role="dialog"
            aria-modal="true"
            aria-labelledby="lab-controls-title"
            data-sheet={primitives.sheet}
            data-dialog={primitives.dialog}
            data-elevation={primitives.elevation}
            data-safe-area={primitives.safeArea}
            data-minimum-target={primitives.minimumTarget}
            onKeyDown={trapFocus}
          >
            <header className="lab-controls__header">
              <div>
                <p className="lab-controls__eyebrow">Local prototype state</p>
                <h2 id="lab-controls-title">Lab Controls</h2>
              </div>
              <button
                ref={closeRef}
                type="button"
                aria-label="Close Lab Controls"
                onClick={() => dispatch({ type: "goBack" })}
              >
                Close
              </button>
            </header>

            <ScenarioControls value={scenario} dispatch={dispatch} />

            <fieldset>
              <legend>Appearance</legend>
              <div className="lab-controls__options">
                {appearances.map((value) => (
                  <label key={value}>
                    <input
                      type="radio"
                      name="appearance"
                      checked={appearance === value}
                      onChange={() =>
                        dispatch({ type: "setAppearance", appearance: value })
                      }
                    />
                    <span>{value}</span>
                  </label>
                ))}
              </div>
            </fieldset>

            <fieldset>
              <legend>Text scale</legend>
              <div className="lab-controls__options">
                {textScales.map((value) => (
                  <label key={value}>
                    <input
                      type="radio"
                      name="text-scale"
                      checked={textScale === value}
                      onChange={() =>
                        dispatch({ type: "setTextScale", textScale: value })
                      }
                    />
                    <span>{value}</span>
                  </label>
                ))}
              </div>
            </fieldset>

            <fieldset>
              <legend>Motion</legend>
              <label className="lab-controls__check">
                <input
                  type="checkbox"
                  checked={reducedMotion}
                  onChange={(event) =>
                    dispatch({
                      type: "setReducedMotion",
                      reducedMotion: event.currentTarget.checked,
                    })
                  }
                />
                <span>Reduce motion</span>
              </label>
            </fieldset>

            <button
              className="lab-controls__reset"
              type="button"
              onClick={() => {
                resetRequestedRef.current = true;
                dispatch({ type: "reset" });
              }}
            >
              Reset prototype
            </button>
          </section>
        </div>
      ) : null}
    </>
  );
}
