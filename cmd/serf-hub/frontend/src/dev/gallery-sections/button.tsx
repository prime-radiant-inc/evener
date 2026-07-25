import { Button, type ButtonVariant } from "../../widgets/button";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const VARIANTS: ButtonVariant[] = ["primary", "quiet", "danger", "dangerQuiet"];

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
      <Button variant={variant} size="md">
        Save changes
      </Button>
      <Button variant={variant} size="sm">
        Save changes
      </Button>
      <Button variant={variant} size="md" icon={<DotIcon />}>
        Save changes
      </Button>
      <Button variant={variant} size="md" disabled>
        Save changes
      </Button>
    </div>
  );
}

export default function ButtonGallerySection() {
  return (
    <section>
      <h2>Button</h2>
      <ThemeFlip>
        {VARIANTS.map((variant) => (
          <VariantRow key={variant} variant={variant} />
        ))}
      </ThemeFlip>
    </section>
  );
}
