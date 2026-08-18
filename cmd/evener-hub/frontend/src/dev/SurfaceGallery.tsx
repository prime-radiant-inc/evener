import type { ComponentType } from "react";
import styles from "./gallery.module.css";

interface GallerySectionModule {
  default: ComponentType;
}

// One module per surface, stream-owned (src/dev/surface-sections/<name>.tsx)
// so adding a surface's gallery section never conflicts with another
// stream's - the same discovery idiom WidgetGallery.tsx uses for
// gallery-sections/*.tsx. Sorted by path so the page order is stable across
// builds. The exclusion pattern keeps a section's own colocated
// *.test.tsx (kata dw3s added the first one, surface-sections/transcript.test.tsx)
// out of the glob - without it, a test module with no default export renders
// as `undefined`, an invalid element type.
const SECTION_MODULES = import.meta.glob<GallerySectionModule>(
  ["./surface-sections/*.tsx", "!./surface-sections/*.test.tsx"],
  { eager: true },
);

const SECTIONS = Object.keys(SECTION_MODULES)
  .sort()
  .map((path) => {
    // path came from Object.keys(SECTION_MODULES) itself, so the lookup
    // always hits - but that's this loop's own invariant, invisible to the
    // indexed-access type. Checked, not asserted.
    const mod = SECTION_MODULES[path];
    if (!mod) throw new Error(`missing surface gallery section module for ${path}`);
    return { path, Section: mod.default };
  });

/**
 * `/dev/surfaces`: renders every registered PANE-LEVEL surface (transcript,
 * composer, chrome, rail, …) with mocked or seeded-store data, the way
 * `/dev/widgets` does for individual widgets. Dev build only - see
 * src/App.tsx, which gates the route (and this whole module's import)
 * behind import.meta.env.DEV so it never reaches a production bundle.
 *
 * Every section renders REAL production components. Nothing here fakes a
 * network call: a section either passes plain fixture props, or seeds a
 * store directly (stores are plain zustand, so a section can call
 * `.setState()` the same way a test's own fixture setup does) - see each
 * surface-sections/*.tsx file's own header for which it does.
 */
export default function SurfaceGallery() {
  return (
    <div className={styles.gallery}>
      <p className={styles.intro}>
        Surface gallery — real pane-level surfaces, rendered with fixture data. Nothing here is live: no network request
        actually resolves, and no action button here has a lasting effect.
      </p>
      {SECTIONS.map(({ path, Section }) => (
        <Section key={path} />
      ))}
    </div>
  );
}
