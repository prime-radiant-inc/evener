import { useState } from "react";
import { Button, MediaLightbox } from "../../widgets";
import { GallerySection } from "../GallerySection";
import styles from "./medialightbox.module.css";

const SAMPLE =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAgAAAAECAIAAAA8r+mnAAAAFUlEQVR4nGP4z/AfK2LAKYFDHLcEAGSoP8FHDbrlAAAAAElFTkSuQmCC";

// Open by default inside a containing-block frame, the same approach as the
// Dialog and Sheet sections: the dialog stays in its own box instead of
// covering the gallery, and closing it reveals a Reopen affordance.
function LightboxDemo() {
  const [open, setOpen] = useState(true);
  return (
    <div className={styles.demoFrame}>
      {!open && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Reopen
        </Button>
      )}
      <MediaLightbox
        open={open}
        onClose={() => setOpen(false)}
        title="Sample image"
        src={SAMPLE}
        alt="Sample chart"
        caption="chart.png"
      >
        <div className={styles.nav}>
          <Button variant="quiet" size="sm" onClick={() => {}}>
            Previous
          </Button>
          <Button variant="quiet" size="sm" onClick={() => {}}>
            Next
          </Button>
        </div>
      </MediaLightbox>
    </div>
  );
}

export default function MediaLightboxGallerySection() {
  return (
    <GallerySection title="MediaLightbox">
      <LightboxDemo />
    </GallerySection>
  );
}
