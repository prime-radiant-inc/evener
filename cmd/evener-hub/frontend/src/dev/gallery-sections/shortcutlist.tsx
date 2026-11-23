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
        {/* sequences: two alternative bindings for one action, a multi-press
            sequence, and the unbound state the cheatsheet shows. */}
        <ShortcutList
          items={[
            {
              sequences: [
                [{ modifiers: ["Control"], optionalModifiers: [], key: "K" }],
                [{ modifiers: ["Control"], optionalModifiers: [], key: "P" }],
              ],
              label: "open the command palette (two bindings)",
            },
            {
              sequences: [
                [
                  { modifiers: [], optionalModifiers: [], key: "g" },
                  { modifiers: [], optionalModifiers: [], key: "g" },
                ],
              ],
              label: "go to line (two presses)",
            },
            { sequences: [], unbound: "Unbound", label: "an action with no binding" },
          ]}
        />
      </ThemeFlip>
    </section>
  );
}
