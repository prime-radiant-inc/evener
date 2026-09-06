import { useState } from "react";
import { FormRow } from "../../widgets/formrow";
import { Input } from "../../widgets/input";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./formrow.module.css";

function LiveFormRow() {
  const [value, setValue] = useState("");
  const invalid = value.length > 0 && value.length < 3;
  return (
    <FormRow
      label="Display name"
      htmlFor="gallery-formrow-live"
      help={invalid ? undefined : "At least three characters."}
      error={invalid ? "Name must contain at least three characters." : undefined}
    >
      <Input
        id="gallery-formrow-live"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="My project"
      />
    </FormRow>
  );
}

export default function FormRowGallerySection() {
  return (
    <section>
      <h2>FormRow</h2>
      <ThemeFlip>
        <div className={styles.stack}>
          <LiveFormRow />
          <FormRow label="Hub address" htmlFor="gallery-formrow-plain">
            <Input id="gallery-formrow-plain" value="127.0.0.1:9180" onChange={() => {}} disabled />
          </FormRow>
        </div>
      </ThemeFlip>
    </section>
  );
}
