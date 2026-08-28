import { Banner } from "../../widgets/banner";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./banner.module.css";

// The banner is position: absolute, so each demo frames it in a relative
// box with a little height so the overlay is visible against content below
// it - the same way AppShell's .shell anchors the production banner. Both
// tones are shown at rest (attention = reconnecting/heads-up, danger =
// closed/broken), and each carries the action shape its real call site uses.
const DEMOS = [
  {
    tone: "attention" as const,
    message: "Reconnecting to the server…",
    action: { label: "Retry now", onClick: () => {} },
  },
  {
    tone: "danger" as const,
    message: "Connection closed.",
    action: { label: "Retry", onClick: () => {} },
  },
  {
    tone: "danger" as const,
    message: "This page is out of date with the server. Reload to continue.",
    action: { label: "Reload", onClick: () => {} },
  },
];

function BannerDemo() {
  return (
    <div className={styles.frame}>
      {DEMOS.map((demo) => (
        <div key={demo.message} className={styles.stage}>
          <div className={styles.underlay}>content the banner overlays</div>
          <Banner tone={demo.tone} message={demo.message} action={demo.action} />
        </div>
      ))}
    </div>
  );
}

export default function BannerGallerySection() {
  return (
    <section data-testid="banner-gallery">
      <h2>Banner</h2>
      <p className={styles.note}>Overlay status strip — floats over content, never pushes it down.</p>
      <ThemeFlip>
        <BannerDemo />
      </ThemeFlip>
    </section>
  );
}
