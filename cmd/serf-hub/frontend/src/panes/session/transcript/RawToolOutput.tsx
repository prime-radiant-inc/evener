// RawToolOutput is the tool-renderer registry's default body (T1 ships
// only this fallback; T3 registers real per-tool bodies - read/grep/shell/
// diff/...). Plain raw output text, no formatting/truncation - exactly
// "fallback body = raw output" per the wave-4 plan.

import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./rawtooloutput.module.css";
import type { ToolRenderProps } from "./toolRenderers";

const CLASS = {
  output: requireClass(styles.output, "rawtooloutput.module.css", "output"),
};

export function RawToolOutput({ item }: ToolRenderProps) {
  return <pre className={CLASS.output}>{item.output ?? ""}</pre>;
}
