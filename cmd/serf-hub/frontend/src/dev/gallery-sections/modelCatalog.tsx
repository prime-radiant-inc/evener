// ModelCatalog is both the component (value) and the envelope interface (type);
// one import brings in both meanings via declaration merging.
import { ModelCatalog } from "../../widgets/modelCatalog";
import { ThemeFlip } from "../ThemeFlip";

// The rich model catalog widget (wave-8 T2 filled it: provider grouping,
// capability badges, cost, and a Recent section). The gallery drives it with a
// small static in-memory fake, just enough to show the two resting displays -
// the harness-default marker and a chosen qualified provider/model; it never
// hits the wire, so the badges/cost/Recent affordances only light up against a
// live /api/models envelope.
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
