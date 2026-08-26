import type { ReactNode, RefObject } from "react";
import { useMemo } from "react";
import type { ThreadModel } from "../../../protocol/model";
import type { TranscriptDisplayConfigV1 } from "../../../transcriptDisplay/config";
import { projectThread } from "../../../transcriptDisplay/projector";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { VirtualList, type VirtualListHandle } from "../../../widgets";
import { modelLabel } from "../chrome/statusFormat";
import { exchangeOpenersFor } from "./exchangeOpeners";
import { FlowOverlay } from "./flow/FlowOverlay";
import { TurnBlock } from "./TurnBlock";
import "./messages";
import "./tools";
import styles from "../session.module.css";

const ESTIMATED_TURN_HEIGHT = 96;

export interface TranscriptBodyProps {
  model: ThreadModel;
  config: TranscriptDisplayConfigV1;
  surface: "live" | "readOnly" | "preview";
  disclosureScope: string;
  sessionRef?: string;
  showSeenDividerTurnId?: string;
  loadOlderRow?: ReactNode;
  liveOverlay?: ReactNode;
  listRef?: RefObject<VirtualListHandle | null>;
  onMeasurementsChange?: () => void;
  trailingContent?: ReactNode;
}

export function TranscriptBody({
  model,
  config,
  surface,
  disclosureScope,
  sessionRef,
  showSeenDividerTurnId,
  loadOlderRow,
  liveOverlay,
  listRef,
  onMeasurementsChange,
  trailingContent,
}: TranscriptBodyProps) {
  const projection = useMemo(() => projectThread(model, config), [model, config]);
  const openers = useMemo(() => exchangeOpenersFor(model.turns), [model.turns]);
  const agentLabel = modelLabel(model.modelProvider, model.model);
  const renderTurn = (index: number) => {
    const projectedTurn = projection.turns[index];
    if (!projectedTurn) throw new Error(`Transcript index ${index} out of range for ${projection.turns.length} turns`);
    return (
      <TurnBlock
        turn={projectedTurn}
        sessionRef={sessionRef}
        showSeenDivider={projectedTurn.id === showSeenDividerTurnId}
        exchangeOpeners={openers}
        agentLabel={agentLabel}
        viewAnchorIndex={index}
      />
    );
  };

  const list = (
    <div className={styles.transcriptList} data-testid="transcript-virtual-list">
      <VirtualList
        ref={listRef}
        dynamic
        anchorToEnd
        count={projection.turns.length}
        estimateSize={() => ESTIMATED_TURN_HEIGHT}
        getItemKey={(index) => projection.turns[index]?.id ?? index}
        renderRow={renderTurn}
        onChange={() => onMeasurementsChange?.()}
      />
    </div>
  );

  const content =
    surface === "preview" ? (
      <div data-testid="transcript-preview-flow">
        {projection.turns.map((_, index) => (
          <div key={projection.turns[index]?.id ?? index}>{renderTurn(index)}</div>
        ))}
      </div>
    ) : (
      <div className={styles.transcript}>
        {surface === "live" ? (
          <FlowOverlay top={loadOlderRow} pill={liveOverlay}>
            <div className={styles.transcriptContent}>
              {list}
              {trailingContent}
            </div>
          </FlowOverlay>
        ) : (
          <>
            {loadOlderRow}
            <div className={styles.transcriptContent}>
              {list}
              {trailingContent}
            </div>
          </>
        )}
      </div>
    );

  return (
    <TranscriptRenderProvider
      config={config}
      projection={projection}
      surface={surface}
      sessionRef={sessionRef}
      disclosureScope={disclosureScope}
    >
      {content}
    </TranscriptRenderProvider>
  );
}
