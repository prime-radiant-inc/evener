import { ToolIcon, type ToolIconKind } from "../../widgets/toolicon";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const KINDS: { kind: ToolIconKind; label: string }[] = [
  { kind: "terminal", label: "terminal (shell)" },
  { kind: "file", label: "file (read_file)" },
  { kind: "edit", label: "edit (edit/write/patch)" },
  { kind: "search", label: "search (grep/glob/web_search)" },
  { kind: "folder", label: "folder (list_dir)" },
  { kind: "globe", label: "globe (web_fetch)" },
  { kind: "ask", label: "ask (ask_user)" },
  { kind: "tasks", label: "tasks (task_list)" },
  { kind: "delegate", label: "delegate (subagent)" },
  { kind: "transcript", label: "transcript (read_transcript)" },
  { kind: "job", label: "job (job_* family)" },
  { kind: "send", label: "send (delegate_send)" },
  { kind: "skill", label: "skill (use_skill)" },
  { kind: "wrench", label: "wrench (default/MCP)" },
  { kind: "thought", label: "thought (thinking row)" },
];

export default function ToolIconGallerySection() {
  return (
    <section>
      <h2>ToolIcon</h2>
      <ThemeFlip>
        {KINDS.map(({ kind, label }) => (
          <div className={styles.row} key={kind}>
            <p className={styles.rowLabel}>{label}</p>
            <ToolIcon kind={kind} />
          </div>
        ))}
        {/* currentColor: the glyph takes whatever ink its consumer sets - the
            transcript rows render it in --ink-mid against both themes. */}
        <div className={styles.row}>
          <p className={styles.rowLabel}>inherits ink</p>
          <span style={{ display: "inline-flex", color: "var(--ink-low)" }}>
            <ToolIcon kind="terminal" />
          </span>
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>size 16</p>
          <ToolIcon kind="wrench" size={16} />
        </div>
      </ThemeFlip>
    </section>
  );
}
