import { AttachIcon } from "../../panes/session/composer/attachments/AttachIcon";
import { GearIcon, MoreIcon, PlusIcon, SearchIcon, SidebarIcon } from "../../shell/rail/railIcons";
import { CloseIcon, IconButton } from "../../widgets";
import type { IconButtonVariant } from "../../widgets/iconbutton";
import { GallerySection } from "../GallerySection";
import styles from "../gallery-section.module.css";

const VARIANTS: IconButtonVariant[] = ["primary", "quiet", "danger", "dangerQuiet"];

function VariantRow({ variant }: { variant: IconButtonVariant }) {
  return (
    <div className={styles.row}>
      <p className={styles.rowLabel}>{variant}</p>
      <IconButton variant={variant} size="md" label="Close" icon={<CloseIcon />} />
      <IconButton variant={variant} size="sm" label="Close" icon={<CloseIcon />} />
      <IconButton variant={variant} size="xs" label="Close" icon={<CloseIcon />} />
      <IconButton variant={variant} size="md" label="Close" icon={<CloseIcon />} disabled />
      <IconButton variant={variant} size="md" label="Close (refused)" icon={<CloseIcon />} aria-disabled />
      <IconButton
        variant={variant}
        size="md"
        label="Close (explained)"
        icon={<CloseIcon />}
        disabled
        disabledReason="Nothing to close in this session"
      />
    </div>
  );
}

export default function IconButtonGallerySection() {
  return (
    <GallerySection title="IconButton">
      <div className={styles.row}>
        <p className={styles.rowLabel}>Workspace actions</p>
        <IconButton label="New session" icon={<PlusIcon />} />
        <IconButton label="Attach image" icon={<AttachIcon />} variant="quiet" />
        <IconButton label="More actions" icon={<MoreIcon />} variant="quiet" />
        <IconButton label="Sessions" icon={<SidebarIcon />} variant="quiet" />
        <IconButton label="Search" icon={<SearchIcon />} variant="quiet" />
        <IconButton label="Settings" icon={<GearIcon />} variant="quiet" />
      </div>
      {VARIANTS.map((variant) => (
        <VariantRow key={variant} variant={variant} />
      ))}
      <div className={styles.row}>
        <p className={styles.rowLabel}>badge</p>
        <IconButton
          variant="secondary"
          size="sm"
          label="Show sidebar (3 need attention)"
          icon={<SidebarIcon />}
          badge={{ count: 3, tone: "attention" }}
        />
        <IconButton
          variant="quiet"
          label="Sessions, 120 need attention"
          icon={<SidebarIcon />}
          badge={{ count: 120, tone: "attention" }}
        />
        <IconButton variant="quiet" label="Sessions, 7 need attention" icon={<SidebarIcon />} badge={{ count: 7 }} />
      </div>
    </GallerySection>
  );
}
