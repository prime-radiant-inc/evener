// ModelCatalog is both the component (value) and the envelope interface (type);
// one import brings in both meanings via declaration merging.
import { useState } from "react";
import { ModelCatalog, type ModelCatalog as ModelCatalogEnvelope, ModelCatalogPanel } from "../../widgets/modelCatalog";
import { Sheet } from "../../widgets/sheet";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./modelCatalog.module.css";

// The rich model catalog widget (wave-8 T2 filled it: provider grouping,
// capability badges, cost, and a Recent section). The gallery drives it with a
// small static in-memory fake, just enough to show the two resting displays -
// the harness-default marker and a chosen qualified provider/model; it never
// hits the wire, so the badges/cost/Recent affordances only light up against a
// live model/list response.
const CATALOG: ModelCatalogEnvelope = {
  models: [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
  ],
  recent: [],
};

function loadCatalog(): Promise<ModelCatalogEnvelope> {
  return Promise.resolve(CATALOG);
}

// The mobile variant: the same catalog rows in a bottom Sheet with 48px tap
// targets and no search input (docs/web-ui/design-system.md §11). Shown the
// same open-by-default, contained-frame way the Sheet section itself is - see
// sheet.module.css in this directory for the containing-block trick.
function SheetDemo() {
  const [open, setOpen] = useState(true);
  return (
    <div className={styles.demoFrame}>
      {!open && (
        <button type="button" className={styles.reopen} onClick={() => setOpen(true)}>
          Reopen sheet
        </button>
      )}
      <Sheet open={open} side="bottom" onClose={() => setOpen(false)} title="Choose model">
        <div className={styles.sheetBody}>
          <ModelCatalogPanel
            loading={false}
            error={null}
            catalog={CATALOG}
            value="anthropic/claude-sonnet-4-5"
            onPick={() => setOpen(false)}
            variant="sheet"
          />
        </div>
      </Sheet>
    </div>
  );
}

export default function ModelCatalogGallerySection() {
  return (
    <section>
      <h2>Model catalog</h2>
      <ThemeFlip>
        <ModelCatalog value="" onChange={() => {}} loadCatalog={loadCatalog} />
        <ModelCatalog value="anthropic/claude-sonnet-4-5" onChange={() => {}} loadCatalog={loadCatalog} />
      </ThemeFlip>
      <h3>Mobile sheet variant</h3>
      <ThemeFlip>
        <SheetDemo />
      </ThemeFlip>
    </section>
  );
}
