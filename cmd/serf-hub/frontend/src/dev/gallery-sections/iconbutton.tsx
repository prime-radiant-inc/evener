import type { ButtonVariant } from "../../widgets/button";
import { IconButton } from "../../widgets/iconbutton";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const VARIANTS: ButtonVariant[] = ["primary", "quiet", "danger"];

function DotIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <circle cx="8" cy="8" r="5" fill="currentColor" />
    </svg>
  );
}

function VariantRow({ variant }: { variant: ButtonVariant }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{variant}</p>
      <IconButton variant={variant} size="md" label="Close" icon={<DotIcon />} />
      <IconButton variant={variant} size="sm" label="Close" icon={<DotIcon />} />
      <IconButton variant={variant} size="md" label="Close" icon={<DotIcon />} disabled />
    </div>
  );
}

export default function IconButtonGallerySection() {
  return (
    <section>
      <h2>IconButton</h2>
      <ThemeFlip>
        {VARIANTS.map((variant) => (
          <VariantRow key={variant} variant={variant} />
        ))}
      </ThemeFlip>
    </section>
  );
}
