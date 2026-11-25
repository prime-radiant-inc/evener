import { ImageThumbnail } from "../../widgets";
import { GallerySection } from "../GallerySection";
import sectionStyles from "../gallery-section.module.css";
import styles from "./imagethumbnail.module.css";

// A tiny real PNG; the widget renders an <img>, and the gallery needs no
// shipped asset for that.
const SAMPLE =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAgAAAAECAIAAAA8r+mnAAAAFUlEQVR4nGP4z/AfK2LAKYFDHLcEAGSoP8FHDbrlAAAAAElFTkSuQmCC";

export default function ImageThumbnailGallerySection() {
  return (
    <GallerySection
      title="ImageThumbnail"
      note={
        <p className={sectionStyles.note}>
          The frame, open trigger, image, and caption, with placeholder and overlay slots.
        </p>
      }
    >
      <div className={sectionStyles.row}>
        <ImageThumbnail
          src={SAMPLE}
          alt="Sample chart"
          onOpen={() => {}}
          frameClassName={styles.thumb}
          imageClassName={styles.image}
        />
        <ImageThumbnail
          src={SAMPLE}
          alt="Sample chart"
          onOpen={() => {}}
          frameClassName={styles.thumb}
          imageClassName={styles.image}
          caption="chart.png"
          captionClassName={styles.caption}
        />
        <ImageThumbnail
          placeholder={<div className={styles.pending} />}
          frameClassName={styles.thumb}
          imageClassName={styles.image}
        />
        <ImageThumbnail
          src={SAMPLE}
          alt="Sample chart"
          onOpen={() => {}}
          frameClassName={styles.positionedThumb}
          imageClassName={styles.image}
          overlay={<span className={styles.badge}>8×4</span>}
        />
      </div>
    </GallerySection>
  );
}
