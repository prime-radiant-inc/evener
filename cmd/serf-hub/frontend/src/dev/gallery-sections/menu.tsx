import { useEffect, useRef } from "react";
import { Menu, type MenuItem } from "../../widgets/menu";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./menu.module.css";

const ITEMS: MenuItem[] = [
  { id: "rename", label: "Rename", onSelect: () => {} },
  { id: "duplicate", label: "Duplicate", onSelect: () => {} },
  { id: "delete", label: "Delete", onSelect: () => {}, disabled: true },
];

// Menu's locked API ({trigger, items}) has no controlled "open" prop - it
// manages its own open/closed state internally, on purpose (a caller
// shouldn't need to reimplement that). To still show it open by default
// here (this task's "render overlays open inline" instruction), the demo
// clicks the real rendered trigger button on mount: the same interaction a
// user takes, found via its aria-haspopup="menu" attribute (part of
// Menu's own accessibility contract, not an incidental implementation
// detail) rather than reaching for an internal prop that doesn't exist.
function MenuDemo() {
  const frameRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    frameRef.current?.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')?.click();
  }, []);

  return (
    <div ref={frameRef} className={styles.frame}>
      <p className={styles.hint}>Click "Actions", or focus it and press ArrowDown/ArrowUp/Enter/Space.</p>
      <Menu trigger="Actions" items={ITEMS} />
    </div>
  );
}

export default function MenuGallerySection() {
  return (
    <section>
      <h2>Menu</h2>
      <ThemeFlip>
        <MenuDemo />
      </ThemeFlip>
    </section>
  );
}
