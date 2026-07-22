import { useState } from "react";
import { RadioGroup, type RadioGroupOption } from "../../widgets/radiogroup";
import { ThemeFlip } from "../ThemeFlip";
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

export default function RadioGroupGallerySection() {
  return (
    <section>
      <h2>RadioGroup</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <LiveRadioGroup />
          <RadioGroup
            label="Loud for (one option disabled)"
            value="asks"
            onChange={() => {}}
            options={LOUD_SCOPE_OPTIONS}
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
