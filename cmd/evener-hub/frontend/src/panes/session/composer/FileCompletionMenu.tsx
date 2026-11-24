import { type FileSearchItem, matchFileMentionPositions } from "@evener/appwire-client";
import { type ReactNode, useEffect, useMemo, useRef } from "react";
import { Loader } from "../../../widgets";
import styles from "./filecompletionmenu.module.css";

const EMPTY_POSITION_SET = new Set<number>();

export function fileOptionId(listboxId: string, index: number): string {
  return `${listboxId}-opt-${index}`;
}

export interface FileCompletionMenuProps {
  id: string;
  query: string;
  items: FileSearchItem[];
  highlightedIndex: number;
  showLoader: boolean;
  truncated: boolean;
  onSelect: (item: FileSearchItem) => void;
}

function renderHighlightedSegment(text: string, baseOffset: number, matched: Set<number>): ReactNode[] {
  if (text.length === 0) return [];
  const runs: Array<{ text: string; active: boolean; offset: number }> = [];
  let runStart = 0;
  let runActive = matched.has(baseOffset);
  for (let i = 1; i < text.length; i += 1) {
    const active = matched.has(baseOffset + i);
    if (active !== runActive) {
      runs.push({ text: text.slice(runStart, i), active: runActive, offset: baseOffset + runStart });
      runStart = i;
      runActive = active;
    }
  }
  runs.push({ text: text.slice(runStart), active: runActive, offset: baseOffset + runStart });

  return runs.map((run) =>
    run.active ? (
      <span key={run.offset} className={styles.match}>
        {run.text}
      </span>
    ) : (
      run.text
    ),
  );
}

export function FileCompletionMenu({
  id,
  query,
  items,
  highlightedIndex,
  showLoader,
  truncated,
  onSelect,
}: FileCompletionMenuProps) {
  const listboxRef = useRef<HTMLDivElement>(null);
  const positionSets = useMemo(
    () =>
      items.map((item) => {
        const positions = matchFileMentionPositions(item.path, query);
        return positions.length === 0 ? EMPTY_POSITION_SET : new Set(positions);
      }),
    [items, query],
  );

  useEffect(() => {
    if (highlightedIndex < 0 || items.length === 0) return;
    const doc = listboxRef.current?.ownerDocument ?? document;
    doc.getElementById(fileOptionId(id, highlightedIndex))?.scrollIntoView?.({ block: "nearest" });
  }, [highlightedIndex, id, items.length]);

  return (
    <div
      ref={listboxRef}
      role="listbox"
      id={id}
      aria-label="Files"
      className={styles.listbox}
      data-testid="composer-file-menu"
    >
      {items.map((item, index) => {
        const active = index === highlightedIndex;
        const displayPath = item.kind === "dir" ? `${item.path}/` : item.path;
        const lastSlash = item.path.lastIndexOf("/");
        const dirPart = lastSlash >= 0 ? item.path.slice(0, lastSlash) : "";
        const baseStart = lastSlash >= 0 ? lastSlash + 1 : 0;
        const basePart = displayPath.slice(baseStart);
        const posSet = positionSets[index] ?? EMPTY_POSITION_SET;

        return (
          // biome-ignore lint/a11y/useFocusableInteractive: WAI-ARIA combobox keeps DOM focus in SkillEditor and tracks virtual focus via aria-activedescendant
          // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard selection is handled by SkillEditor via aria-activedescendant
          <div
            key={item.path}
            role="option"
            id={fileOptionId(id, index)}
            aria-selected={active}
            className={active ? `${styles.option} ${styles.optionActive}` : styles.option}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onSelect(item)}
          >
            <span className={styles.basename}>{renderHighlightedSegment(basePart, baseStart, posSet)}</span>
            {dirPart && <span className={styles.directory}>{renderHighlightedSegment(dirPart, 0, posSet)}</span>}
          </div>
        );
      })}
      {showLoader && items.length === 0 && (
        <div className={styles.loaderRow}>
          <Loader label="Searching files" />
        </div>
      )}
      {truncated && items.length > 0 && (
        <div role="note" className={styles.status}>
          Showing top matches from the first 25,000 scanned paths; keep typing to narrow.
        </div>
      )}
    </div>
  );
}
