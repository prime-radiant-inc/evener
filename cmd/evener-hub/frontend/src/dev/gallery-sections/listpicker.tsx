import { useState } from "react";
import { Button } from "../../widgets/button";
import { ListPicker, type ListPickerOption } from "../../widgets/listpicker";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

function LiveListPicker({ id, initial = "local" }: { id: string; initial?: string }) {
  const [value, setValue] = useState(initial);
  const options: ListPickerOption[] = [
    { id: "local", label: "magic-kingdom (hub)", onSelect: () => setValue("local") },
    { id: "buildbox", label: "buildbox", onSelect: () => setValue("buildbox") },
    {
      id: "offline-host",
      label: "offline-host",
      meta: "Offline",
      action: (
        <Button variant="quiet" size="xs" type="button">
          Connect
        </Button>
      ),
    },
  ];
  return (
    <ListPicker
      id={id}
      label="Host"
      value={value}
      options={options}
      footer={{ label: "Connect host", onSelect: () => {} }}
    />
  );
}

export default function ListPickerGallerySection() {
  return (
    <section>
      <h2>ListPicker</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>the hub selected, with an offline host and a footer action</p>
          <LiveListPicker id="gallery-host-local" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>a remote host selected</p>
          <LiveListPicker id="gallery-host-remote" initial="buildbox" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>disabled</p>
          <ListPicker
            id="gallery-host-disabled"
            label="Host"
            value="local"
            options={[{ id: "local", label: "magic-kingdom (hub)", onSelect: () => {} }]}
            disabled
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
