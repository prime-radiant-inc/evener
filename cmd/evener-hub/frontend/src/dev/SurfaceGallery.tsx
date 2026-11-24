import styles from "./gallery.module.css";
import { type GallerySectionEntry, SURFACE_GALLERY_SECTIONS } from "./galleryDiscovery";

// The shared discovery module owns the glob and the sort; these names stay
// here so a caller (and the test) imports the section list and its type from
// the gallery it configures.
export { SURFACE_GALLERY_SECTIONS };
export type SurfaceGallerySection = GallerySectionEntry;

/**
 * `/dev/surfaces`: renders every registered PANE-LEVEL surface (transcript,
 * composer, chrome, rail, …) with mocked or seeded-store data, the way
 * `/dev/widgets` does for individual widgets. Dev build only - see
 * src/App.tsx, which gates the route (and this whole module's import)
 * behind import.meta.env.DEV so it never reaches a production bundle.
 * One module per surface, stream-owned (src/dev/surface-sections/<name>.tsx),
 * so adding a section never conflicts with another stream's.
 *
 * Every section renders REAL production components. Nothing here fakes a
 * network call: a section either passes plain fixture props, or seeds a
 * store directly (stores are plain zustand, so a section can call
 * `.setState()` the same way a test's own fixture setup does) - see each
 * surface-sections/*.tsx file's own header for which it does.
 */
interface SurfaceGalleryProps {
  sections?: readonly SurfaceGallerySection[];
}

export default function SurfaceGallery({ sections = SURFACE_GALLERY_SECTIONS }: SurfaceGalleryProps = {}) {
  return (
    <div className={styles.gallery}>
      <p className={styles.intro}>
        Surface gallery — real pane-level surfaces, rendered with fixture data. Nothing here is live: no network request
        actually resolves, and no action button here has a lasting effect.
      </p>
      {sections.map(({ path, Section }) => (
        <Section key={path} />
      ))}
    </div>
  );
}
