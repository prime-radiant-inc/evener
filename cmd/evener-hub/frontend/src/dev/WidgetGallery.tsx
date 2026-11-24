import styles from "./gallery.module.css";
import { WIDGET_GALLERY_SECTIONS } from "./galleryDiscovery";

/**
 * `/dev/widgets`: renders every registered widget gallery section. Dev
 * build only — see src/App.tsx, which gates the route (and this whole
 * module's import) behind import.meta.env.DEV so it never reaches a
 * production bundle.
 * One module per widget, stream-owned (src/dev/gallery-sections/<name>.tsx),
 * so adding a section never conflicts with another stream's.
 */
export default function WidgetGallery() {
  return (
    <div className={styles.gallery}>
      <p className={styles.intro}>Widget gallery — every widget, every documented state, both themes side by side.</p>
      {WIDGET_GALLERY_SECTIONS.map(({ path, Section }) => (
        <Section key={path} />
      ))}
    </div>
  );
}
