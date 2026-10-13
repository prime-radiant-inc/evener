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
      <IconButton
        variant={variant}
        size="md"
        label="Close (explained)"
        icon={<DotIcon />}
        disabled
        disabledReason="Nothing to close in this session"
      />
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
        <div className={styles.row}>
          <p className={styles.rowLabel}>badge</p>
          <IconButton
            variant="secondary"
            size="sm"
            label="Show sidebar (3 need attention)"
            icon={<DotIcon />}
            badge={{ count: 3, tone: "attention" }}
          />
          <IconButton variant="quiet" label="Sessions" icon={<DotIcon />} badge={{ count: 120, tone: "attention" }} />
          <IconButton variant="quiet" label="Inbox" icon={<DotIcon />} badge={{ count: 7 }} />
        </div>
      </ThemeFlip>
    </section>
  );
}
