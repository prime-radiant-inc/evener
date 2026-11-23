import { useState } from "react";
import { Switch } from "../../widgets/switch";
import { GallerySection } from "../GallerySection";
import styles from "./switch.module.css";

function LiveSwitch() {
  const [checked, setChecked] = useState(false);
  return <Switch label="Notifications" checked={checked} onChange={setChecked} />;
}

export default function SwitchGallerySection() {
  return (
    <GallerySection title="Switch">
      <div className={styles.row}>
        <LiveSwitch />
        <Switch label="Always on" checked={true} onChange={() => {}} />
        <Switch label="Disabled off" checked={false} onChange={() => {}} disabled />
        <Switch label="Disabled on" checked={true} onChange={() => {}} disabled />
        <Switch
          label="Sound"
          checked={false}
          onChange={() => {}}
          description="Short tone when a session needs you. Descriptions wrap under the label, not the track."
        />
      </div>
    </GallerySection>
  );
}
