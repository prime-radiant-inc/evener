import { Button } from "../../widgets/button";
import { Cadence } from "../../widgets/cadence";
import { PaneScaffold } from "../../widgets/panescaffold";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./panescaffold.module.css";

const NOW = 1_700_000_000_000;

function MinimalPane() {
  return (
    <PaneScaffold title="Quick notes">
      <p>Just a title and a body - the floor every other pane builds on.</p>
    </PaneScaffold>
  );
}

function FullChromePane() {
  return (
    <PaneScaffold
      title="A session title long enough to truncate in this fixed-width pane"
      cadence={<Cadence state="working" frameTimes={[NOW, NOW - 1_500, NOW - 3_000]} now={NOW} />}
      actions={
        <Button variant="quiet" size="sm">
          Close
        </Button>
      }
      footer={
        <div className={styles.footerRow}>
          <span>3 of 12</span>
          <span>Updated just now</span>
        </div>
      }
    >
      <div className={styles.longBody}>
        {Array.from({ length: 12 }, (_, i) => (
          <p key={i}>Body line {i + 1} - enough of these to make the body scroll independently.</p>
        ))}
      </div>
    </PaneScaffold>
  );
}

export default function PaneScaffoldGallerySection() {
  return (
    <section>
      <h2>PaneScaffold</h2>
      <ThemeFlip>
        <div className={styles.frame}>
          <MinimalPane />
        </div>
        <div className={styles.frame}>
          <FullChromePane />
        </div>
      </ThemeFlip>
    </section>
  );
}
