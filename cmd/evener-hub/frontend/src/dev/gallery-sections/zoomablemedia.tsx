import { useState } from "react";
import { Dialog, ZoomableMedia } from "../../widgets";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./zoomablemedia.module.css";

// An inline svg stands in for the image so the gallery needs no asset.
function Picture() {
  return (
    <svg className={styles.picture} viewBox="0 0 160 90" role="img" aria-label="Sample chart">
      <rect width="160" height="90" fill="var(--surface-2)" />
      <path d="M10 75 L50 40 L90 55 L150 15" stroke="var(--accent)" strokeWidth="4" fill="none" />
    </svg>
  );
}

// The three shapes the app uses: a full-width image (the doc pane), a
// fixed thumbnail tile it fills (the composer's attachment), and a
// thumbnail named by its own alt text and caption (the transcript gallery).
function ZoomableMediaDemo() {
  const [open, setOpen] = useState(false);
  return (
    <div className={styles.frame}>
      <ZoomableMedia label="Zoom image" onOpen={() => setOpen(true)}>
        <Picture />
      </ZoomableMedia>
      <div className={styles.tile}>
        <ZoomableMedia label="View chart.png" onOpen={() => setOpen(true)}>
          <Picture />
        </ZoomableMedia>
      </div>
      <div className={styles.tile}>
        <ZoomableMedia onOpen={() => setOpen(true)}>
          <Picture />
          <span className={styles.caption}>chart.png</span>
        </ZoomableMedia>
      </div>
      {open && (
        <Dialog open onClose={() => setOpen(false)} title="Sample chart" size="large">
          <Picture />
        </Dialog>
      )}
    </div>
  );
}

export default function ZoomableMediaGallerySection() {
  return (
    <section>
      <h2>ZoomableMedia</h2>
      <ThemeFlip>
        <ZoomableMediaDemo />
      </ThemeFlip>
    </section>
  );
}
