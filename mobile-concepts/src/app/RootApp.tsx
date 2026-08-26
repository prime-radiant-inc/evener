import { type ReactNode, useCallback } from "react";
import { conceptRegistry } from "../concepts/registry";
import type { ConceptId } from "../core/model";
import type { PlatformPrimitives } from "../core/platform";
import type { PrototypeAction } from "../core/state";
import { usePrototypeState } from "../core/store";
import { ConceptGallery } from "./ConceptGallery";
import { ConceptSwitcher } from "./ConceptSwitcher";

export interface RootAppProps {
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
}

export function RootApp({ primitives, dispatch }: RootAppProps) {
  const state = usePrototypeState((current) => current);
  const concept = state.concept;
  const modalActive = state.overlay !== null;

  const selectFirstConcept = useCallback(
    (selectedConcept: ConceptId) => {
      dispatch({ type: "selectConcept", concept: selectedConcept });
      dispatch({ type: "navigateRoot", tab: "sessions" });
    },
    [dispatch],
  );
  const openConceptSwitcher = useCallback(
    () => dispatch({ type: "openOverlay", overlay: "concept-switcher" }),
    [dispatch],
  );
  const openLabControls = useCallback(
    () => dispatch({ type: "openOverlay", overlay: "lab-controls" }),
    [dispatch],
  );
  const closeOverlay = useCallback(
    () => dispatch({ type: "goBack" }),
    [dispatch],
  );

  let content: ReactNode;
  if (concept === null) {
    content = (
      <ConceptGallery
        primitives={primitives}
        dispatch={dispatch}
        onSelectConcept={selectFirstConcept}
      />
    );
  } else {
    const { Renderer } = conceptRegistry[concept];
    content = (
      <Renderer
        state={state}
        dispatch={dispatch}
        platform={state.platform}
        primitives={primitives}
        onOpenConceptSwitcher={openConceptSwitcher}
        onOpenLabControls={openLabControls}
      />
    );
  }

  return (
    <>
      <div
        data-testid="foundation-background"
        aria-hidden={modalActive || undefined}
        inert={modalActive || undefined}
      >
        {content}
      </div>
      <ConceptSwitcher
        open={state.overlay === "concept-switcher"}
        primitives={primitives}
        dispatch={dispatch}
        onClose={closeOverlay}
      />
    </>
  );
}
