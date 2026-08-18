import { useRef } from "react";
import { Button } from "../../widgets/button";
import { VirtualList, type VirtualListHandle } from "../../widgets/virtuallist";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./virtuallist.module.css";

const ROW_COUNT = 10_000;
const ROW_HEIGHT = 32;

function VirtualListDemo() {
  const listRef = useRef<VirtualListHandle>(null);

  return (
    <div>
      <div className={styles.frame}>
        <VirtualList
          ref={listRef}
          count={ROW_COUNT}
          estimateSize={() => ROW_HEIGHT}
          renderRow={(index) => (
            <div className={index % 2 === 0 ? styles.row : `${styles.row} ${styles.shaded}`}>
              Row {index.toLocaleString()}
            </div>
          )}
        />
      </div>
      <Button
        variant="quiet"
        size="sm"
        onClick={() => listRef.current?.scrollToIndex(ROW_COUNT - 1, { align: "start" })}
      >
        Jump to last row
      </Button>
    </div>
  );
}

export default function VirtualListGallerySection() {
  return (
    <section>
      <h2>VirtualList</h2>
      <ThemeFlip>
        <VirtualListDemo />
      </ThemeFlip>
    </section>
  );
}
