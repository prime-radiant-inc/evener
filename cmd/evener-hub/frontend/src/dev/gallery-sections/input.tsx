import { useState } from "react";
import { Input } from "../../widgets/input";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

function LiveInput({ placeholder, ariaLabel }: { placeholder?: string; ariaLabel?: string }) {
  const [value, setValue] = useState("");
  return (
    <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={placeholder} aria-label={ariaLabel} />
  );
}

export default function InputGallerySection() {
  return (
    <GallerySection title="Input">
      <div className={styles.row}>
        <p className={styles.rowLabel}>empty</p>
        <LiveInput placeholder="Search…" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>aria-label, no visible label</p>
        <LiveInput placeholder="Filter settings" ariaLabel="Filter settings" />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>filled</p>
        <Input value="evener-hub" onChange={() => {}} />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>disabled</p>
        <Input value="read only" onChange={() => {}} disabled />
      </div>
    </GallerySection>
  );
}
