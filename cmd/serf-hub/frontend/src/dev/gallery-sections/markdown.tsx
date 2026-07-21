import { Markdown } from "../../widgets/markdown";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./markdown.module.css";

const SAMPLE = `# Session summary

The agent read \`config.yaml\`, ran the build, and opened a **pull request**.
See the [diff on GitHub](https://github.com/example/serf/pull/42) for the
full change.

## What changed

- Replaced the retired \`v1\` client with \`v2\`
- Added a *regression test* for the timeout path
- Removed ~~the temporary workaround~~

> One test still flakes under load; tracked separately.

\`\`\`go
func main() {
	fmt.Println("hello, serf")
}
\`\`\`

---

Next: rerun \`make test\` after the dependency bump.
`;

export default function MarkdownGallerySection() {
  return (
    <section>
      <h2>Markdown</h2>
      <ThemeFlip>
        <div className={styles.frame}>
          <Markdown source={SAMPLE} />
        </div>
      </ThemeFlip>
    </section>
  );
}
