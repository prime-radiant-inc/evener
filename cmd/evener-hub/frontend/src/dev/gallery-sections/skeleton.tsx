import { Skeleton } from "../../widgets/skeleton";
import { GallerySection } from "../GallerySection";
import styles from "./skeleton.module.css";

export default function SkeletonGallerySection() {
  return (
    <GallerySection title="Skeleton">
      <div className={styles.row}>
        <div className={styles.sample}>
          <Skeleton lines={1} />
        </div>
        <div className={styles.sample}>
          <Skeleton />
        </div>
        <div className={styles.sample}>
          <Skeleton lines={5} />
        </div>
      </div>
    </GallerySection>
  );
}
