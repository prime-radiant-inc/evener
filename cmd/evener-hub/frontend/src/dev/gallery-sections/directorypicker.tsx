import { ThemeFlip } from "../ThemeFlip";
import { LivePathField } from "./pathfield";

export default function DirectoryPickerSection() {
  return (
    <section>
      <h2>DirectoryPicker</h2>
      <p>Shared directory browsing and creation. Use this folder confirms; Cancel preserves the field.</p>
      <ThemeFlip>
        <LivePathField kind="dir" initial="/opt" withRecents />
        <LivePathField kind="dir" initial="" />
      </ThemeFlip>
    </section>
  );
}
