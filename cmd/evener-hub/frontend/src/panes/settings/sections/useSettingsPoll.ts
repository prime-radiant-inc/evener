// Settings polls run only while the pane's own document is visible (useVisibleInterval).
import { usePaneDocument } from "../../../shell/paneDocument";
import { useVisibleInterval } from "../../../shell/useVisibleInterval";

export function useSettingsPoll(intervalMs: number, tick: () => void): void {
  useVisibleInterval(tick, intervalMs, true, usePaneDocument());
}
