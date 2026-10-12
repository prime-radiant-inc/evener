import { ShortcutList } from "../../widgets/shortcutlist";
import { ThemeFlip } from "../ThemeFlip";

export default function ShortcutListGallerySection() {
  return (
    <section>
      <h2>ShortcutList</h2>
      <ThemeFlip>
        <ShortcutList
          items={[
            { keys: ["Mod", "K"], label: "open the command palette" },
            { keys: ["Shift", "Enter"], label: "jump to a turn in the current session" },
            { keys: ["Esc"], label: "close the palette (or back out of args mode)" },
          ]}
        />
      </ThemeFlip>
    </section>
  );
}
