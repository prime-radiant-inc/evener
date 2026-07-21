import { useState } from "react";
import { Select } from "../../widgets/select";
import { ThemeFlip } from "../ThemeFlip";
import styles from "../gallery-section.module.css";

const OPTIONS = [
  { value: "us-east", label: "US East" },
  { value: "us-west", label: "US West" },
  { value: "eu-central", label: "EU Central" },
];

function LiveSelect() {
  const [value, setValue] = useState("us-east");
  return <Select value={value} options={OPTIONS} onChange={(e) => setValue(e.target.value)} />;
}

export default function SelectGallerySection() {
  return (
    <section>
      <h2>Select</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>enabled</p>
          <LiveSelect />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>disabled</p>
          <Select value="us-east" options={OPTIONS} onChange={() => {}} disabled />
        </div>
      </ThemeFlip>
    </section>
  );
}
