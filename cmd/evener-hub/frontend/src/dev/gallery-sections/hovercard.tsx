import { EntityRef } from "../../panes/session/transcript/EntityRef";
import {
  delegateView,
  SUMMARY_ENTITY_JOB,
  summaryEntityView,
} from "../../panes/session/transcript/entityView.testFixture";
import { HoverCard } from "../../widgets/hovercard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./tooltip.module.css";

// The entity-card demo renders EntityRef itself with the shared test fixtures,
// not a hand-rolled lookalike, so this section can never drift from what the
// tests actually pin. Resolved once at module scope (the fixtures are
// constant); triggerOnly keeps the demo a pure hover target - no Open button
// wiring a click into the workspace store from inside the gallery.
const jobDemo = summaryEntityView().get(SUMMARY_ENTITY_JOB);
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
          <button type="button">job_01HZX7P9</button>
        </HoverCard>{" "}
        · <EntityRef view={jobDemo} id={SUMMARY_ENTITY_JOB} triggerOnly /> ·{" "}
        <EntityRef view={delegateDemo} id="dlg_gallery_demo" triggerOnly />
      </p>
    </div>
  );
}

export default function HoverCardGallerySection() {
  return (
    <section>
      <h2>HoverCard</h2>
      <ThemeFlip>
        <HoverCardDemo />
      </ThemeFlip>
    </section>
  );
}
