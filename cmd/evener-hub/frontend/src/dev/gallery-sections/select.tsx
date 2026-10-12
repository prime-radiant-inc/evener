import { useState } from "react";
import { Select, type SelectOption } from "../../widgets/select";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const OPTIONS = [
  { value: "us-east", label: "US East" },
  { value: "us-west", label: "US West" },
  { value: "eu-central", label: "EU Central" },
];

const HOST_OPTIONS = [
  { value: "local", label: "This hub" },
  { value: "build-box", label: "build-box" },
  { value: "gpu-box", label: "gpu-box (offline)", disabled: true },
];

function LiveSelect({ options = OPTIONS }: { options?: SelectOption[] }) {
  const [value, setValue] = useState(options[0]?.value ?? "");
  return <Select value={value} options={options} onChange={(e) => setValue(e.target.value)} />;
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
          <p className={styles.rowLabel}>a disabled option</p>
          <LiveSelect options={HOST_OPTIONS} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>disabled</p>
          <Select value="us-east" options={OPTIONS} onChange={() => {}} disabled />
        </div>
      </ThemeFlip>
    </section>
  );
}
