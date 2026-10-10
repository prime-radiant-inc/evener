// The automatic memory refresh renderer: a systemMessage with the typed
// eventKind "memory-context" (from agent/session_memory.go's MEMORY_CONTEXT
// turn) shown as a compact, steering-style "Refreshed my memory" notification.
// Collapsed at every verbosity level, including Full, and independent of any
// general expansion baseline - only an explicit reader choice opens it.
//
// Opening reveals the scope and index state, then the decoded index through the
// existing safe Markdown renderer. A raw that does not decode keeps the compact
// notification and shows its complete original Text, so an unfamiliar record is
// never discarded or turned into invented index data. Even on a valid record a
// folded "Source" exposes the complete original Text: the Markdown sanitizer
// strips task checkboxes and images, so the literal recorded message is the only
// place checked/unchecked markers, image alt/title, escaped source and the
// delegate read-only suffix stay inspectable.

import {
  type ItemModel,
  MEMORY_CONTEXT_LABEL,
  type MemoryContextObservation,
  memoryContextEmptyText,
  memoryContextScopeLabel,
  memoryContextStateLabel,
  parseMemoryContext,
} from "@evener/appwire-client";
import { Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./memorycontextitem.module.css";
import { SteeringDivider } from "./SteeringDivider";

const CLASS = {
  body: requireClass(styles.body, "memorycontextitem.module.css", "body"),
  meta: requireClass(styles.meta, "memorycontextitem.module.css", "meta"),
  state: requireClass(styles.state, "memorycontextitem.module.css", "state"),
  truncated: requireClass(styles.truncated, "memorycontextitem.module.css", "truncated"),
  content: requireClass(styles.content, "memorycontextitem.module.css", "content"),
  empty: requireClass(styles.empty, "memorycontextitem.module.css", "empty"),
  fallback: requireClass(styles.fallback, "memorycontextitem.module.css", "fallback"),
  source: requireClass(styles.source, "memorycontextitem.module.css", "source"),
  sourceSummary: requireClass(styles.sourceSummary, "memorycontextitem.module.css", "sourceSummary"),
  sourceText: requireClass(styles.sourceText, "memorycontextitem.module.css", "sourceText"),
};

// The folded literal source: the complete recorded Text, verbatim. Native
// <details>, collapsed by default, independent of the shared disclosure store.
function MemorySource({ text }: { text: string }) {
  return (
    <details className={CLASS.source} data-testid="memory-context-source">
      <summary className={CLASS.sourceSummary}>Source</summary>
      <pre className={CLASS.sourceText} data-testid="memory-context-source-text">
        {text}
      </pre>
    </details>
  );
}

export function MemoryContextBody({
  item,
  observation,
}: {
  item: ItemModel;
  observation: MemoryContextObservation | undefined;
}) {
  if (!observation) {
    // Decode failure: the compact notification stays, and opening shows the
    // complete original Text.
    return (
      <div className={CLASS.body} data-testid="memory-context-body">
        <pre className={CLASS.fallback} data-testid="memory-context-fallback">
          {item.text}
        </pre>
      </div>
    );
  }
  const meta = `${memoryContextScopeLabel(observation.scope)} · ${memoryContextStateLabel(observation.state)}`;
  const hasContent = observation.content.trim() !== "";
  return (
    <div className={CLASS.body} data-testid="memory-context-body">
      <div className={CLASS.meta} data-testid="memory-context-meta">
        <span data-testid="memory-context-scope-state">{meta}</span>
        {observation.truncated && (
          <span className={CLASS.truncated} data-testid="memory-context-truncated">
            truncated
          </span>
        )}
      </div>
      {hasContent ? (
        <div className={CLASS.content} data-testid="memory-context-content">
          <Markdown source={observation.content} />
        </div>
      ) : (
        <p className={CLASS.empty} data-testid="memory-context-empty">
          {memoryContextEmptyText(observation.state)}
        </p>
      )}
      <MemorySource text={item.text} />
    </div>
  );
}

export function MemoryContextDisclosure({ item, sessionRef }: { item: ItemModel; sessionRef?: string }) {
  const observation = parseMemoryContext(item.raw);
  // Unavailable or revoked observations carry that state on the collapsed row
  // too, so the summary never implies a successful read.
  const qualifier =
    observation && (observation.state === "unavailable" || observation.state === "revoked") ? (
      <span className={CLASS.state} data-testid="memory-context-state">
        {memoryContextStateLabel(observation.state)}
      </span>
    ) : undefined;
  return (
    <SteeringDivider
      id={item.id}
      label={MEMORY_CONTEXT_LABEL}
      labelTestId="memory-context-label"
      testId="memory-context-item"
      sessionRef={sessionRef}
      ignoreBaseline
      qualifiers={qualifier}
      body={<MemoryContextBody item={item} observation={observation} />}
    />
  );
}
