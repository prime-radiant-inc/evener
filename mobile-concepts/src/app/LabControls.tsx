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

  return (
    <>
      <button
        className="lab-controls-trigger"
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() =>
          dispatch({ type: "openOverlay", overlay: "lab-controls" })
        }
      >
        Lab Controls
      </button>
      {open ? (
        <div className="lab-controls-backdrop">
          <section
            className="lab-controls"
            role="dialog"
            aria-modal="true"
            aria-labelledby="lab-controls-title"
            data-sheet={primitives.sheet}
            data-dialog={primitives.dialog}
            data-elevation={primitives.elevation}
            data-safe-area={primitives.safeArea}
            data-minimum-target={primitives.minimumTarget}
          >
            <header className="lab-controls__header">
              <div>
                <p className="lab-controls__eyebrow">Local prototype state</p>
                <h2 id="lab-controls-title">Lab Controls</h2>
              </div>
              <button
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
              onClick={() => dispatch({ type: "reset" })}
            >
              Reset prototype
            </button>
          </section>
        </div>
      ) : null}
    </>
  );
}
