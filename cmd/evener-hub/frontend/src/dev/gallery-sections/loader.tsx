import { Loader } from "../../widgets/loader";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

// A fixed anchor instant, not Date.now() (same reasoning as cadence.tsx's
// own gallery section): Loader is a pure render of its props, so there's no
// reason for its sample data - including the "frozen" elapsed readout - to
// look different on every reload.
const STARTED_AT = 1_700_000_000_000;

const SAMPLES: { label: string; loaderLabel?: string; startedAt?: number; now?: number }[] = [
  { label: "default", loaderLabel: undefined, startedAt: undefined, now: undefined },
  { label: "custom label, no elapsed", loaderLabel: "Starting agent" },
  { label: "elapsed only", startedAt: STARTED_AT, now: STARTED_AT + 5_000 },
  { label: "label + elapsed", loaderLabel: "Fetching diff", startedAt: STARTED_AT, now: STARTED_AT + 83_000 },
];

export default function LoaderGallerySection() {
  return (
    <GallerySection title="Loader">
      {SAMPLES.map(({ label, loaderLabel, startedAt, now }) => (
        <div className={styles.row} key={label}>
          <p className={styles.rowLabel}>{label}</p>
          <Loader label={loaderLabel} startedAt={startedAt} now={now} />
        </div>
      ))}
    </GallerySection>
  );
}
