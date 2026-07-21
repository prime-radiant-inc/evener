import { Skeleton } from "../../widgets/skeleton";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./skeleton.module.css";

export default function SkeletonGallerySection() {
  return (
    <section>
      <h2>Skeleton</h2>
      <ThemeFlip>
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
      </ThemeFlip>
    </section>
  );
}
