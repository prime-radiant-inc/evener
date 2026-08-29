import {
  type KeyboardEvent,
  type ReactElement,
  useLayoutEffect,
  useMemo,
  useRef,
} from "react";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "../../components/timeline/VariableHeightVirtualList";
import { Sheet } from "../../ui/Sheet";
import type { EvidenceDisplayItem, EvidenceSection } from "../model";

const MAX_MOUNTED_EVIDENCE_SECTIONS = 48;
const ESTIMATED_EVIDENCE_SECTION_PX = 120;

export interface ActivityEvidenceSheetProps {
  readonly open: boolean;
  readonly evidence: EvidenceDisplayItem | null;
  readonly triggerKey: string | null;
  readonly onClose: () => void;
  readonly onTriggerUnavailable: () => void;
}

interface EvidenceSectionRow {
  readonly key: string;
  readonly section: EvidenceSection;
  readonly logicalPosition: number;
}

interface EvidenceProjection {
  readonly instanceKey: string;
  readonly rows: readonly EvidenceSectionRow[];
}

function triggerForKey(key: string): HTMLElement | null {
  return (
    [
      ...document.querySelectorAll<HTMLElement>("[data-evidence-trigger-key]"),
    ].find((candidate) => candidate.dataset.evidenceTriggerKey === key) ?? null
  );
}

function EvidenceSectionView({ row }: { readonly row: EvidenceSectionRow }) {
  return (
    <section
      className="live-conversation-evidence__section"
      aria-label={`Evidence section ${row.logicalPosition}`}
      data-evidence-section="true"
      tabIndex={-1}
    >
      <h3>{row.section.heading.text}</h3>
      <p>{row.section.body.text}</p>
    </section>
  );
}

export function ActivityEvidenceSheet({
  open,
  evidence,
  triggerKey,
  onClose,
  onTriggerUnavailable,
}: ActivityEvidenceSheetProps): ReactElement {
  const listRef = useRef<VariableHeightVirtualListHandle>(null);
  const plainDetailRef = useRef<HTMLElement>(null);
  const evidenceProjectionsRef = useRef(
    new WeakMap<EvidenceDisplayItem, EvidenceProjection>(),
  );
  const nextEvidenceIdentityRef = useRef(0);
  const nextOccurrenceIdentityRef = useRef(0);
  const previousOpenRef = useRef(false);
  const closingTriggerRef = useRef<string | null>(null);

  const projection = useMemo(() => {
    if (evidence === null) return null;
    const existing = evidenceProjectionsRef.current.get(evidence);
    if (existing !== undefined) return existing;
    nextEvidenceIdentityRef.current += 1;
    const instanceKey = `evidence-instance:${nextEvidenceIdentityRef.current}`;
    const rows: EvidenceSectionRow[] = [];
    for (const section of evidence.sections) {
      nextOccurrenceIdentityRef.current += 1;
      rows.push({
        key: `${instanceKey}:occurrence:${nextOccurrenceIdentityRef.current}`,
        section,
        logicalPosition: rows.length + 1,
      });
    }
    const created = {
      instanceKey,
      rows,
    } satisfies EvidenceProjection;
    evidenceProjectionsRef.current.set(evidence, created);
    return created;
  }, [evidence]);
  const rows = projection?.rows ?? [];

  useLayoutEffect(() => {
    if (open) {
      previousOpenRef.current = true;
      closingTriggerRef.current = triggerKey;
      return;
    }
    if (!previousOpenRef.current) return;
    previousOpenRef.current = false;
    const closingTrigger = closingTriggerRef.current;
    closingTriggerRef.current = null;
    if (closingTrigger !== null && triggerForKey(closingTrigger) === null) {
      onTriggerUnavailable();
    }
  }, [onTriggerUnavailable, open, triggerKey]);

  const focusFinalSection = (event: KeyboardEvent<HTMLElement>): void => {
    if (event.key !== "End" || rows.length === 0) return;
    const finalRow = rows.at(-1);
    if (finalRow === undefined) return;
    event.preventDefault();
    if (rows.length > MAX_MOUNTED_EVIDENCE_SECTIONS) {
      listRef.current?.focusKey(finalRow.key);
      return;
    }
    const sections = plainDetailRef.current?.querySelectorAll<HTMLElement>(
      '[data-evidence-section="true"]',
    );
    if (sections === undefined) return;
    sections.item(sections.length - 1)?.focus({ preventScroll: true });
  };

  const detail =
    rows.length > MAX_MOUNTED_EVIDENCE_SECTIONS ? (
      <VariableHeightVirtualList
        key={projection?.instanceKey}
        ref={listRef}
        items={rows}
        getItemKey={(row) => row.key}
        estimateSize={() => ESTIMATED_EVIDENCE_SECTION_PX}
        measurementCache={{ mode: "ephemeral" }}
        rowSemantics={{ mode: "caller-owned" }}
        scrollSemantics={{
          mode: "custom",
          role: "region",
          ariaLabel: "Activity and evidence details",
          pageScrollOwner: true,
          locked: false,
        }}
        overscan={6}
        maxMountedRows={48}
        onScroll={() => {}}
        resolveFocusTarget={(rowElement) =>
          rowElement.querySelector<HTMLElement>(
            '[data-evidence-section="true"]',
          )
        }
        renderItem={(row) => <EvidenceSectionView row={row} />}
      />
    ) : (
      <section
        ref={plainDetailRef}
        className="live-conversation-evidence__plain-detail"
        aria-label="Activity and evidence details"
        data-page-scroll-owner="true"
        tabIndex={-1}
        onKeyDown={focusFinalSection}
      >
        {rows.map((row) => (
          <EvidenceSectionView key={row.key} row={row} />
        ))}
      </section>
    );

  return (
    <Sheet
      open={open && evidence !== null}
      onClose={onClose}
      title={evidence?.title.text ?? "Activity and evidence"}
      dialogClassName="live-conversation-evidence"
      dialogDataAttrs={{ "data-activity-evidence-sheet": "true" }}
    >
      {evidence !== null ? (
        <>
          <h2>{evidence.title.text}</h2>
          <p className="live-conversation-evidence__state">
            State: {evidence.redacted ? "Redacted" : "Available"}
          </p>
          {detail}
          <button
            type="button"
            aria-label="Close activity and evidence"
            onKeyDown={focusFinalSection}
            onClick={onClose}
          >
            Close activity and evidence
          </button>
        </>
      ) : null}
    </Sheet>
  );
}
