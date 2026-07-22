// ModelCatalog is both the component (value) and the envelope interface (type);
// one import brings in both meanings via declaration merging.
import { ModelCatalog } from "../../widgets/modelCatalog";
import { ThemeFlip } from "../ThemeFlip";

// Interim-Combobox stub (T1); wave-8 T2 fills the rich catalog (provider
// grouping, capability badges, cost, Recent) inside the same widget. The
// gallery shows its two resting displays: the harness-default marker, and a
// chosen qualified provider/model. loadCatalog is a static in-memory fake -
// the gallery never hits the wire.
const CATALOG: ModelCatalog = {
  models: [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
  ],
  recent: [],
};

function loadCatalog(): Promise<ModelCatalog> {
  return Promise.resolve(CATALOG);
}

export default function ModelCatalogGallerySection() {
  return (
    <section>
      <h2>Model catalog</h2>
      <ThemeFlip>
        <ModelCatalog value="" onChange={() => {}} loadCatalog={loadCatalog} />
        <ModelCatalog value="anthropic/claude-sonnet-4-5" onChange={() => {}} loadCatalog={loadCatalog} />
      </ThemeFlip>
    </section>
  );
}
