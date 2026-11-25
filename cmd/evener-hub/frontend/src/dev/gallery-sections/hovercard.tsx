import { findEntityView } from "@evener/appwire-client";
import { EntityRef } from "../../panes/session/transcript/EntityRef";
import {
  delegateView,
  SUMMARY_ENTITY_JOB,
  summaryEntityView,
} from "../../panes/session/transcript/entityView.testFixture";
import { Button } from "../../widgets/button";
import { HoverCard } from "../../widgets/hovercard";
import { GallerySection } from "../GallerySection";
import styles from "./tooltip.module.css";

// The entity-card demo renders EntityRef itself with the shared test fixtures,
// not a hand-rolled lookalike, so this section can never drift from what the
// tests actually pin. Resolved once at module scope (the fixtures are
// constant); triggerOnly keeps the demo a pure hover target - no Open button
// wiring a click into the workspace store from inside the gallery.
const jobDemo = findEntityView(summaryEntityView(), "job", SUMMARY_ENTITY_JOB, "local:s");
if (!jobDemo) throw new Error("gallery: shared job fixture did not resolve");
const delegateDemo = delegateView("dlg_gallery_demo", {}, { task: "Review the hover card pass", runningForMs: 42_000 });

function HoverCardDemo() {
  return (
    <div className={styles.demo}>
      <p className={styles.hint}>
        Hover or focus a trigger. The bare widget takes any content; the entity references render the real production
        card over the shared test fixtures.
      </p>
      <p>
        <HoverCard label={<div>Tests passed in 14 seconds</div>}>
          <Button variant="quiet" size="sm">
            job_01HZX7P9
          </Button>
        </HoverCard>{" "}
        · <EntityRef view={jobDemo} id={SUMMARY_ENTITY_JOB} triggerOnly /> ·{" "}
        <EntityRef view={delegateDemo} id="dlg_gallery_demo" triggerOnly />
      </p>
      <p className={styles.hint}>
        A block wrapper fills its container. Both dashed rows are 240px wide; only the block trigger stretches to the
        row's edge.
      </p>
      <div
        style={{
          maxWidth: 240,
          padding: "var(--space-1)",
          border: "1px dashed var(--edge)",
        }}
      >
        <HoverCard label={<div>Shrink-wrapped trigger</div>}>
          <Button variant="quiet" size="sm" style={{ width: "100%" }}>
            Shrink-wrapped
          </Button>
        </HoverCard>
      </div>
      <div
        style={{
          maxWidth: 240,
          marginTop: "var(--space-2)",
          padding: "var(--space-1)",
          border: "1px dashed var(--edge)",
        }}
      >
        <HoverCard label={<div>Block trigger</div>} block>
          <Button variant="quiet" size="sm" block>
            Block fills the row
          </Button>
        </HoverCard>
      </div>
    </div>
  );
}

export default function HoverCardGallerySection() {
  return (
    <GallerySection title="HoverCard">
      <HoverCardDemo />
    </GallerySection>
  );
}
