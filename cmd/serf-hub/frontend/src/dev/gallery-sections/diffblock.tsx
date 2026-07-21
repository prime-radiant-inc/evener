import { DiffBlock } from "../../widgets/diffblock";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./diffblock.module.css";

// No blank lines in the hunk body: a real diff represents an unchanged
// blank source line as a context line containing a single space, which is
// too easy for an editor/formatter to silently strip from this file - so
// this fixture sticks to non-blank lines throughout, still realistic.
const SAMPLE_DIFF = `diff --git a/internal/greet/greet.go b/internal/greet/greet.go
index 3b18e51..a1b2c3d 100644
--- a/internal/greet/greet.go
+++ b/internal/greet/greet.go
@@ -1,7 +1,9 @@
 package greet
 import "fmt"
-func Greet() string {
-	return "hello"
+func Greet(name string) string {
+	if name == "" {
+		name = "world"
+	}
+	return fmt.Sprintf("hello, %s", name)
 }
`;

export default function DiffBlockGallerySection() {
  return (
    <section>
      <h2>DiffBlock</h2>
      <ThemeFlip>
        <div className={styles.frame}>
          <DiffBlock unified={SAMPLE_DIFF} />
        </div>
      </ThemeFlip>
    </section>
  );
}
