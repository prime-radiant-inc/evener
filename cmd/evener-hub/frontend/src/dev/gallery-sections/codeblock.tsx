import { CodeBlock } from "../../widgets/codeblock";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./codeblock.module.css";

const GO_SAMPLE = `func main() {
	fmt.Println("hello, evener")
}`;

const SHELL_SAMPLE = `$ npm run test
 Test Files  9 passed (9)
      Tests  100 passed (100)`;

// The fold window is 100 lines: a long raw-output dump (a pytest traceback
// shape) folds to its tail only past it - this row shows that folded state
// in the gallery the way every other documented state is shown. The failure
// blocks are generated so the sample clears the window without 100+
// hand-written lines of mock output.
const PYTEST_FAILURES = 20;
const PYTEST_PASSED = 4;
const LONG_PYTEST_SAMPLE = [
  "============================= test session starts ==============================",
  `collected ${PYTEST_FAILURES + PYTEST_PASSED} items`,
  "",
  `tests/test_widgets.py ${"F".repeat(PYTEST_FAILURES)}${".".repeat(PYTEST_PASSED)}                              [100%]`,
  "",
  ...Array.from({ length: PYTEST_FAILURES }, (_, block) => [
    `________________________________ test_case_${block + 1} _________________________________`,
    "",
    `    def test_case_${block + 1}():`,
    ">       assert meter.value == 50",
    "E       assert 42 == 50",
    "",
    `tests/test_widgets.py:${88 + block * 16}: AssertionError`,
  ]).flat(),
  "",
  `=========================== ${PYTEST_FAILURES} failed, ${PYTEST_PASSED} passed in 0.41s ===========================`,
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
          <p className={styles.rowLabel}>long output, folded to its tail (past 100 lines)</p>
          <CodeBlock text={LONG_PYTEST_SAMPLE} copyLabel="Copy output" />
        </div>
      </ThemeFlip>
    </section>
  );
}
