import { CodeBlock } from "../../widgets/codeblock";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./codeblock.module.css";

const GO_SAMPLE = `func main() {
	fmt.Println("hello, serf")
}`;

const SHELL_SAMPLE = `$ npm run test
 Test Files  9 passed (9)
      Tests  100 passed (100)`;

// 67zh: a long raw-output dump (a pytest traceback shape) folds to its tail
// by default past 14 lines - this row shows that folded state in the
// gallery the way every other documented state is shown.
const LONG_PYTEST_SAMPLE = [
  "============================= test session starts ==============================",
  "collected 18 items",
  "",
  "tests/test_widgets.py ......F..F.......                                  [100%]",
  "",
  "=================================== FAILURES ===================================",
  "________________________________ test_meter_fill _________________________________",
  "",
  "    def test_meter_fill():",
  ">       assert meter.value == 50",
  "E       assert 42 == 50",
  "",
  "tests/test_widgets.py:88: AssertionError",
  "________________________________ test_chip_remove _________________________________",
  "",
  "    def test_chip_remove():",
  ">       assert chip.removed",
  "E       AssertionError",
  "",
  "tests/test_widgets.py:104: AssertionError",
  "=========================== 2 failed, 16 passed in 0.41s ===========================",
].join("\n");

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
        <div className={styles.row}>
          <p className={styles.rowLabel}>long output, folded to its tail (67zh)</p>
          <CodeBlock text={LONG_PYTEST_SAMPLE} copyLabel="Copy output" />
        </div>
      </ThemeFlip>
    </section>
  );
}
