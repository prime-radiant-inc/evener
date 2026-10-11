import { MermaidDiagram } from "../../widgets/mermaid";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

// The inline diagram's own gallery section (every src/widgets/* directory needs
// one). The Markdown section shows the fence-in-prose cases; this section shows
// the widget standalone: a diagram that renders and the error fallback for a
// source mermaid cannot parse.
const BENIGN = ["graph TD", "  A[Start] --> B[Decision]", "  B --> C[End]"].join("\n");
const INVALID = "graph TD; A[unclosed";

export default function MermaidGallerySection() {
  return (
    <section>
      <h2>Mermaid</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>flowchart</p>
          <MermaidDiagram source={BENIGN} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>invalid (error fallback)</p>
          <MermaidDiagram source={INVALID} />
        </div>
      </ThemeFlip>
    </section>
  );
}
