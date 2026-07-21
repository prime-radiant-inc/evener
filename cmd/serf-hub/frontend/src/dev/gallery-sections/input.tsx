import { useState } from "react";
import { Input } from "../../widgets/input";
import { ThemeFlip } from "../ThemeFlip";
import styles from "../gallery-section.module.css";

function LiveInput({ placeholder }: { placeholder?: string }) {
  const [value, setValue] = useState("");
  return <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={placeholder} />;
}

export default function InputGallerySection() {
  return (
    <section>
      <h2>Input</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>empty</p>
          <LiveInput placeholder="Search…" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>filled</p>
          <Input value="serf-hub" onChange={() => {}} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>disabled</p>
          <Input value="read only" onChange={() => {}} disabled />
        </div>
      </ThemeFlip>
    </section>
  );
}
