import type { ConceptId } from "../core/model";
import type { PlatformPrimitives } from "../core/platform";
import type { PrototypeAction } from "../core/state";
import { usePrototypeState } from "../core/store";
import { conceptMetadata } from "./conceptMetadata";

export const conceptIds = [
  "stillwater",
  "constellation",
  "field-notes",
] as const satisfies readonly ConceptId[];

interface ConceptCardsProps {
  selectedConcept: ConceptId | null;
  onSelect(concept: ConceptId): void;
  galleryFocusTarget?: boolean;
  disabled?: boolean;
}

export function ConceptCards({
  selectedConcept,
  onSelect,
  galleryFocusTarget = false,
  disabled = false,
}: ConceptCardsProps) {
  return conceptIds.map((conceptId) => {
    const metadata = conceptMetadata[conceptId];
    const selected = selectedConcept === conceptId;
    return (
      <article
        className="concept-card"
        data-accent={metadata.accent}
        data-selected={selected}
        key={conceptId}
      >
        <div className="concept-card__preview" aria-hidden="true">
          <span className="concept-card__number">{metadata.number}</span>
          <span className="concept-card__line concept-card__line--strong" />
          <span className="concept-card__line" />
          <span className="concept-card__line concept-card__line--short" />
        </div>
        <p className="concept-card__family">{metadata.family}</p>
        <h2>{metadata.name}</h2>
        <p className="concept-card__thesis">{metadata.thesis}</p>
        <button
          className="concept-card__select"
          type="button"
          data-gallery-focus-target={
            galleryFocusTarget && conceptId === "stillwater"
              ? "true"
              : undefined
          }
          aria-pressed={selected}
          disabled={disabled}
          onClick={() => onSelect(conceptId)}
        >
          Select {metadata.name}
        </button>
      </article>
    );
  });
}

export interface ConceptGalleryProps {
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
  onSelectConcept?(concept: ConceptId): void;
}

export function ConceptGallery({
  primitives,
  dispatch,
  onSelectConcept,
}: ConceptGalleryProps) {
  const selectedConcept = usePrototypeState((state) => state.concept);

  return (
    <main
      className="concept-gallery"
      data-testid="concept-gallery"
      data-minimum-target={primitives.minimumTarget}
      data-navigation={primitives.navigation}
      data-title={primitives.title}
      data-feedback={primitives.feedback}
      aria-labelledby="concept-gallery-title"
    >
      <header className="concept-gallery__header">
        <p className="concept-gallery__eyebrow">Direct comparison lab</p>
        <h1 id="concept-gallery-title">Choose a design direction</h1>
        <p>
          Three compositions. One deterministic fixture. Compare the interaction
          language before choosing a direction.
        </p>
      </header>
      <div className="concept-gallery__grid">
        <ConceptCards
          selectedConcept={selectedConcept}
          galleryFocusTarget
          onSelect={(concept) => {
            if (onSelectConcept) onSelectConcept(concept);
            else dispatch({ type: "selectConcept", concept });
          }}
        />
      </div>
    </main>
  );
}
