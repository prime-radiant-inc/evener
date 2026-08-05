import { useState } from "react";
import { FormRow } from "../../widgets/formrow";
import { Input } from "../../widgets/input";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./formrow.module.css";

function LiveFormRow() {
  const [value, setValue] = useState("");
  const invalid = value.length > 0 && !value.startsWith("/");
  return (
    <FormRow
      label="Plugin directory"
      htmlFor="gallery-formrow-live"
      help={invalid ? undefined : "Absolute path on the hub's filesystem."}
      error={invalid ? "Path must be absolute." : undefined}
    >
      <Input
        id="gallery-formrow-live"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="/opt/plugins"
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
