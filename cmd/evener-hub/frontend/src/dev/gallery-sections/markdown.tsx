import { Markdown } from "../../widgets/markdown";
import { GallerySection } from "../GallerySection";
import styles from "./markdown.module.css";

const SAMPLE = `# Session summary

The agent read \`config.yaml\`, ran the build, and opened a **pull request**.
See the [diff on GitHub](https://github.com/example/evener/pull/42) for the
full change.

## What changed

- Replaced the retired \`v1\` client with \`v2\`
- Added a *regression test* for the timeout path
- Removed ~~the temporary workaround~~

> One test still flakes under load; tracked separately.

| Step | Command | Result |
| :--- | :--- | :---: |
| build | \`make build\` | ok |
| test | \`make test\` | ok |
| lint | \`make lint\` | ok |

\`\`\`go
func main() {
\tfmt.Println("hello, evener")
}
\`\`\`

---

Next: rerun \`make test\` after the dependency bump.
`;

// Mermaid is a Markdown feature (a ```mermaid fence renders inline between
// prose segments), so its documented states live here in the Markdown section.
// The known trade-off this block exists to eyeball is the spacing at a segment
// boundary: the inline diagram is a sibling of the prose slices, so margins
// between a diagram and the paragraph (or heading) around it are worth a look.
const MERMAID_BETWEEN_PROSE = [
  "Before the diagram, a sentence of prose establishes the reading flow.",
  "",
  "```mermaid",
  "graph TD",
  "  A[Request] --> B{Authorized?}",
  "  B -->|yes| C[Render]",
  "  B -->|no| D[Reject]",
  "```",
  "",
  "After the diagram, more prose follows so the diagram sits between two paragraphs.",
].join("\n");

// The heading-immediately-after case: the segment boundary directly below a
// diagram and directly above a heading is the pseudo-class spacing check.
const MERMAID_BEFORE_HEADING = [
  "```mermaid",
  "graph LR",
  "  A[Start] --> B[Finish]",
  "```",
  "",
  "## Heading right after a diagram",
  "",
  "A paragraph beneath the heading, so the diagram-then-heading boundary is visible.",
].join("\n");

const MERMAID_INVALID = ["```mermaid", "graph TD; A[unclosed", "```"].join("\n");

const MERMAID_WIDE = [
  "```mermaid",
  "graph LR",
  "  A[Stage one] --> B[Stage two] --> C[Stage three] --> D[Stage four] --> E[Stage five]",
  "```",
].join("\n");

const WIDE_TABLE = [
  "| Variant | Pass | Input | Total | Cached | Output | Ratio | Latency |",
  "| :--- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
  '| `descriptive-ethos-bullets` ("You are a collaborator.") | 9,541 (3/3) | 9,277 (-43.9%) | 13,868 (-15.6%) | 1,889 (-17.2%) | 685 (reas 409) | 0.799x (-20.1%) | 13.4 s / 93.2 s |',
  "| `baseline-prompt` | 9,541 (3/3) | 16,536 (0.0%) | 16,431 (0.0%) | 2,281 (0.0%) | 712 (reas 420) | 1.000x (0.0%) | 16.8 s / 112.4 s |",
].join("\n");

export default function MarkdownGallerySection() {
  return (
    <GallerySection title="Markdown">
      <div className={styles.frame}>
        <Markdown source={SAMPLE} />
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Mermaid: prose, flowchart, prose</p>
        <div className={styles.frame}>
          <Markdown source={MERMAID_BETWEEN_PROSE} />
        </div>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Mermaid: diagram then a heading</p>
        <div className={styles.frame}>
          <Markdown source={MERMAID_BEFORE_HEADING} />
        </div>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Mermaid: invalid source (error fallback)</p>
        <div className={styles.frame}>
          <Markdown source={MERMAID_INVALID} />
        </div>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Mermaid: wide diagram</p>
        <div className={styles.wide}>
          <Markdown source={MERMAID_WIDE} />
        </div>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Wide table: narrow container (list reflow)</p>
        <div className={styles.frame}>
          <Markdown source={WIDE_TABLE} />
        </div>
      </div>
      <div className={styles.row}>
        <p className={styles.rowLabel}>Wide table: wide container (tabular)</p>
        <div className={styles.wide}>
          <Markdown source={WIDE_TABLE} />
        </div>
      </div>
    </GallerySection>
  );
}
