import { GallerySection } from "../GallerySection";
import { LivePathField } from "./pathfield";

export default function DirectoryPickerSection() {
  return (
    <GallerySection
      title="DirectoryPicker"
      note={<p>Shared directory browsing and creation. Use this folder confirms; Cancel preserves the field.</p>}
    >
      <LivePathField kind="dir" initial="/opt" withRecents />
      <LivePathField kind="dir" initial="" />
    </GallerySection>
  );
}
