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
        {/* layout="inline": the aside under a cold pane's one action. */}
        <ShortcutList
          layout="inline"
          items={[
            { keys: ["Mod", "K"], label: "command palette" },
            { keys: ["Mod", "N"], label: "new session" },
            { keys: ["?"], label: "all shortcuts" },
          ]}
        />
      </ThemeFlip>
    </section>
  );
}
