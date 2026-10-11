import type { IconButtonVariant } from "../../widgets/iconbutton";
import { IconButton } from "../../widgets/iconbutton";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const VARIANTS: IconButtonVariant[] = ["primary", "quiet", "danger", "dangerQuiet"];

function DotIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <circle cx="8" cy="8" r="5" fill="currentColor" />
    </svg>
  );
}

function VariantRow({ variant }: { variant: IconButtonVariant }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{variant}</p>
      <IconButton variant={variant} size="md" label="Close" icon={<DotIcon />} />
      <IconButton variant={variant} size="sm" label="Close" icon={<DotIcon />} />
      <IconButton variant={variant} size="xs" label="Close" icon={<DotIcon />} />
      <IconButton variant={variant} size="md" label="Close" icon={<DotIcon />} disabled />
      <IconButton variant={variant} size="md" label="Close (refused)" icon={<DotIcon />} aria-disabled />
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
