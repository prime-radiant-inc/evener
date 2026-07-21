import { CodeBlock } from "../../widgets/codeblock";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./codeblock.module.css";

const GO_SAMPLE = `func main() {
	fmt.Println("hello, serf")
}`;

const SHELL_SAMPLE = `$ npm run test
 Test Files  9 passed (9)
      Tests  100 passed (100)`;

export default function CodeBlockGallerySection() {
  return (
    <section>
      <h2>CodeBlock</h2>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>with a language label</p>
          <CodeBlock text={GO_SAMPLE} language="go" />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>no language label (e.g. raw tool output)</p>
          <CodeBlock text={SHELL_SAMPLE} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>with a line-number gutter</p>
          <CodeBlock text={GO_SAMPLE} language="go" showLineNumbers />
        </div>
      </ThemeFlip>
    </section>
  );
}
