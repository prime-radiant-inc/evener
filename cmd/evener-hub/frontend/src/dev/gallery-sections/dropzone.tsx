import { useState } from "react";
import { Dropzone } from "../../widgets/dropzone";
import { GallerySection } from "../GallerySection";
import styles from "./dropzone.module.css";

function LiveDropzone() {
  const [names, setNames] = useState<string[]>([]);
  return (
    <Dropzone onFiles={(files) => setNames(files.map((f) => f.name))}>
      <div className={styles.zoneBody}>
        <p>Drag files here.</p>
        {names.length > 0 && <p className={styles.dropped}>Dropped: {names.join(", ")}</p>}
      </div>
    </Dropzone>
  );
}

export default function DropzoneGallerySection() {
  return (
    <GallerySection title="Dropzone">
      <div className={styles.row}>
        <LiveDropzone />
      </div>
      <div className={styles.row}>
        <Dropzone onFiles={() => {}} disabled>
          <div className={styles.zoneBody}>
            <p>Disabled - dragging has no effect.</p>
          </div>
        </Dropzone>
      </div>
    </GallerySection>
  );
}
