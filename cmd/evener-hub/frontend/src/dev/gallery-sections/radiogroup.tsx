import { useState } from "react";
import { RadioGroup, type RadioGroupOption } from "../../widgets/radiogroup";
import { GallerySection } from "../GallerySection";
import styles from "./radiogroup.module.css";

const THEME_OPTIONS: RadioGroupOption[] = [
  { value: "system", label: "System" },
  { value: "dark", label: "Dark" },
  { value: "light", label: "Light" },
];

const LOUD_SCOPE_OPTIONS: RadioGroupOption[] = [
  { value: "asks", label: "Questions & errors" },
  { value: "all", label: "Everything needing me", disabled: true },
];

function LiveRadioGroup() {
  const [value, setValue] = useState("system");
  return <RadioGroup label="Color theme" value={value} onChange={setValue} options={THEME_OPTIONS} />;
}

// The list variant: each option is a full-width ListRow radio with a trailing
// selected mark, as the phone's setting sheets use it.
function LiveListRadioGroup() {
  const [value, setValue] = useState("dark");
  return (
    <div style={{ width: "min(100%, 22rem)" }}>
      <RadioGroup variant="list" label="Color theme (list)" value={value} onChange={setValue} options={THEME_OPTIONS} />
    </div>
  );
}

export default function RadioGroupGallerySection() {
  return (
    <GallerySection title="RadioGroup">
      <div className={styles.row}>
        <LiveRadioGroup />
        <LiveListRadioGroup />
        <RadioGroup
          label="Loud for (one option disabled)"
          value="asks"
          onChange={() => {}}
          options={LOUD_SCOPE_OPTIONS}
        />
        <RadioGroup
          label="Transcript width"
          value="reading"
          onChange={() => {}}
          options={[
            { value: "reading", label: "Reading" },
            { value: "wide", label: "Wide" },
          ]}
          description="Reading keeps lines near 90 characters. Wide uses more of the window."
        />
      </div>
    </GallerySection>
  );
}
