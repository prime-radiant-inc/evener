import { Button, type ButtonVariant } from "../../widgets/button";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

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
      <Button variant={variant} size="xs">
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

function LinkRow() {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>link</p>
      <p>
        Couldn't load older turns. <Button variant="link">Retry</Button>
      </p>
      <p>
        <Button variant="link" disabled>
          Retry
        </Button>
      </p>
    </div>
  );
}

function WrapRow() {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>wrap</p>
      <div className={styles.narrowColumn}>
        <Button wrap>Jump back in: Move the settings host picker onto the shared Select</Button>
        <Button variant="secondary" size="sm" wrap>
          New session in a much longer project directory name
        </Button>
      </div>
    </div>
  );
}

function BlockRow() {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>block</p>
      <div className={styles.narrowColumn}>
        <Button variant="secondary" block>
          + New session
        </Button>
        <Button variant="quiet" block>
          Test credentials
        </Button>
      </div>
    </div>
  );
}

export default function ButtonGallerySection() {
  return (
    <GallerySection title="Button">
      {VARIANTS.map((variant) => (
        <VariantRow key={variant} variant={variant} />
      ))}
      <WrapRow />
      <BlockRow />
      <LinkRow />
    </GallerySection>
  );
}
