import { useState } from "react";
import { Textarea } from "../../widgets/textarea";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./textarea.module.css";

function LiveTextarea() {
  const [value, setValue] = useState("Type a few lines -\nthis one grows to fit them.");
  return <Textarea value={value} onChange={(e) => setValue(e.target.value)} autoGrow />;
}

export default function TextareaGallerySection() {
  return (
    <section>
      <h2>Textarea</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>fixed rows</p>
          <Textarea value="Two rows, always." onChange={() => {}} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>autoGrow</p>
          <LiveTextarea />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>disabled</p>
          <Textarea value="Read only." onChange={() => {}} disabled />
        </div>
        {/* seamless draws no box of its own, so it only reads correctly inside
            a card that draws one - the wrapper here stands in for the
            composer's inputCard. */}
        <div className={styles.row}>
          <p className={styles.rowLabel}>seamless (in a card)</p>
          <div className={styles.card}>
            <Textarea value="No box of my own." onChange={() => {}} seamless />
          </div>
        </div>
      </ThemeFlip>
    </section>
  );
}
