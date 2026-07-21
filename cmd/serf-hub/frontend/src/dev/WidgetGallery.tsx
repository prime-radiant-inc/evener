import type { ComponentType } from "react";
import styles from "./gallery.module.css";

interface GallerySectionModule {
  default: ComponentType;
}

// One module per widget, stream-owned (src/dev/gallery-sections/<name>.tsx)
// so adding a widget's gallery section never conflicts with another
// stream's. Sorted by path so the page order is stable across builds.
const SECTION_MODULES = import.meta.glob<GallerySectionModule>("./gallery-sections/*.tsx", { eager: true });

const SECTIONS = Object.keys(SECTION_MODULES)
  .sort()
  .map((path) => ({ path, Section: SECTION_MODULES[path]!.default }));

/**
 * `/dev/widgets`: renders every registered widget gallery section. Dev
 * build only — see src/App.tsx, which gates the route (and this whole
 * module's import) behind import.meta.env.DEV so it never reaches a
 * production bundle.
 */
export default function WidgetGallery() {
  return (
    <div className={styles.gallery}>
      <p className={styles.intro}>
        Widget gallery — every widget, every documented state, both themes side by side.
      </p>
      {SECTIONS.map(({ path, Section }) => (
        <Section key={path} />
      ))}
    </div>
  );
}
