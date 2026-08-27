import { useState } from "react";
import {
  contentSummary,
  type HubTranscriptDisplayDefault,
  type TranscriptDisplayCategory,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
  visibleCategoryInventory,
} from "../../../transcriptDisplay/config";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import { Button, Card } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { TranscriptBody } from "../../session/transcript/TranscriptBody";
import { TranscriptDetailEditor } from "../../session/transcript/TranscriptDetailEditor";
import styles from "./transcriptDisplayCard.module.css";

export interface TranscriptDisplayCardProps {
  layout: ViewportClass;
  confirmed: HubTranscriptDisplayDefault;
  draft?: TranscriptDisplayConfigV1;
  localOverride?: TranscriptDisplayConfigV1;
  saveState: "idle" | "saving" | "error";
  error?: string;
  disabled?: boolean;
  onChange(config: TranscriptDisplayConfigV1): void;
  onRetry(): void;
}

const CLASS = {
  content: requireClass(styles.content, "transcriptDisplayCard.module.css", "content"),
  heading: requireClass(styles.heading, "transcriptDisplayCard.module.css", "heading"),
  controls: requireClass(styles.controls, "transcriptDisplayCard.module.css", "controls"),
  preview: requireClass(styles.preview, "transcriptDisplayCard.module.css", "preview"),
  example: requireClass(styles.example, "transcriptDisplayCard.module.css", "example"),
  mobileCanvas: requireClass(styles.mobileCanvas, "transcriptDisplayCard.module.css", "mobileCanvas"),
  inventory: requireClass(styles.inventory, "transcriptDisplayCard.module.css", "inventory"),
  inventoryRow: requireClass(styles.inventoryRow, "transcriptDisplayCard.module.css", "inventoryRow"),
  scope: requireClass(styles.scope, "transcriptDisplayCard.module.css", "scope"),
  status: requireClass(styles.status, "transcriptDisplayCard.module.css", "status"),
  error: requireClass(styles.error, "transcriptDisplayCard.module.css", "error"),
};

const CATEGORY_LABELS: Readonly<Record<TranscriptDisplayCategory, string>> = {
  userMessages: "User messages",
  agentMessages: "Agent messages",
  criticalRows: "Critical rows",
  toolIntent: "Tool intent",
  toolCalls: "Tool calls",
  reasoning: "Reasoning",
  expandedDetails: "Expanded details",
  roundTimings: "Round timings",
  tokenCounts: "Token counts",
  estimatedCost: "Estimated cost",
  systemEvents: "Low-level system events",
  promptEvents: "System prompt and prompt-loaded events",
  hookExits: "Hook exits",
};

function categoryList(categories: readonly TranscriptDisplayCategory[]): string {
  return categories.length === 0 ? "None" : categories.map((category) => CATEGORY_LABELS[category]).join(", ");
}

function layoutLabel(layout: ViewportClass): string {
  return layout === "desktop" ? "Desktop" : "Mobile";
}

/**
 * A hub-default editor and production-shaped preview. The preview model is
 * created once per card so disclosure interactions remain local to this card,
 * while each card still receives an isolated TranscriptBody scope.
 */
export function TranscriptDisplayCard({
  layout,
  confirmed,
  draft,
  localOverride,
  saveState,
  error,
  disabled = false,
  onChange,
  onRetry,
}: TranscriptDisplayCardProps) {
  const [previewModel] = useState(makeTranscriptPreviewModel);
  const config = draft ?? confirmed.config;
  const inventory = visibleCategoryInventory(config);
  const name = layoutLabel(layout);
  const cardId = `transcript-display-card-${layout}`;

  return (
    <article data-testid={cardId} aria-labelledby={`${cardId}-heading`}>
      <Card>
        <div className={CLASS.content}>
          <header className={CLASS.heading}>
            <h2 id={`${cardId}-heading`}>{name} default</h2>
            <span>Hub revision {confirmed.revision}</span>
          </header>

          <div className={CLASS.controls} data-testid={`transcript-display-controls-${layout}`}>
            <TranscriptDetailEditor value={config} onChange={onChange} compact={false} disabled={disabled} />
          </div>

          {localOverride === undefined ? (
            <p className={CLASS.scope}>
              No browser-local live override is set for this layout. This browser follows the hub default unless a live
              transcript choice is made.
            </p>
          ) : (
            <p className={CLASS.scope}>
              A browser-local live view is overriding this hub default ({contentSummary(localOverride.content)}).
              Changing this card does not replace that local choice.
            </p>
          )}

          {saveState === "saving" && (
            <p className={CLASS.status} role="status" aria-live="polite">
              Saving hub default…
            </p>
          )}
          {saveState === "error" && (
            <div className={CLASS.error} role="alert">
              <span>{error ?? "Could not save this hub default."}</span>
              <Button size="sm" variant="secondary" onClick={onRetry} disabled={disabled}>
                Retry
              </Button>
            </div>
          )}

          <section className={CLASS.preview} aria-labelledby={`${cardId}-example-heading`}>
            <div
              className={layout === "mobile" ? `${CLASS.example} ${CLASS.mobileCanvas}` : CLASS.example}
              data-testid={`transcript-display-preview-canvas-${layout}`}
            >
              <h3 id={`${cardId}-example-heading`}>Example only—not your data</h3>
              <div data-testid={`transcript-display-preview-${layout}`}>
                <TranscriptBody
                  model={previewModel}
                  config={config}
                  surface="preview"
                  disclosureScope={`settings:transcript-display:${layout}`}
                  sessionRef={`settings-preview:${layout}`}
                />
              </div>
            </div>
            <div className={CLASS.inventory}>
              <p className={CLASS.inventoryRow}>
                <strong>Shown:</strong> {categoryList(inventory.visible)}
              </p>
              <p className={CLASS.inventoryRow}>
                <strong>Hidden:</strong> {categoryList(inventory.hidden)}
              </p>
            </div>
          </section>
        </div>
      </Card>
    </article>
  );
}

export default TranscriptDisplayCard;
