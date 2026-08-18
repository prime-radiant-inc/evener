import { useState } from "react";
import { Switch } from "../../widgets/switch";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./switch.module.css";

function LiveSwitch() {
  const [checked, setChecked] = useState(false);
  return <Switch label="Notifications" checked={checked} onChange={setChecked} />;
}

export default function SwitchGallerySection() {
  return (
    <section>
      <h2>Switch</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <LiveSwitch />
          <Switch label="Always on" checked={true} onChange={() => {}} />
          <Switch label="Disabled off" checked={false} onChange={() => {}} disabled />
          <Switch label="Disabled on" checked={true} onChange={() => {}} disabled />
        </div>
      </ThemeFlip>
    </section>
  );
}
