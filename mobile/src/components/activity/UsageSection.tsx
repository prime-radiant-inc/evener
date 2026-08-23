import { type JSX, useState } from "react";
import type { UsageSummary } from "../../services/activity";
import { SectionHeader } from "./ActivitySheet";

export interface UsageSectionProps {
  readonly usage: UsageSummary;
}

/**
 * Usage section — tokens (input, output, cache, total), cost, duration, and
 * context pressure. The collapsed summary shows total tokens, cost, and
 * context pressure; expanded shows the full breakdown.
 */
export function UsageSection({ usage }: UsageSectionProps): JSX.Element {
  const [expanded, setExpanded] = useState(false);

  const summaryParts: string[] = [];
  if (usage.totalTokens !== undefined)
    summaryParts.push(String(usage.totalTokens));
  if (usage.cost !== undefined) summaryParts.push(usage.cost);
  if (usage.contextPressure !== undefined) {
    summaryParts.push(`${Math.round(usage.contextPressure * 100)}%`);
  }
  const summary = summaryParts.length > 0 ? summaryParts.join(" · ") : "";

  return (
    <section className="evener-activity-section">
      <SectionHeader
        label="Usage"
        expanded={expanded}
        onToggle={() => setExpanded((e) => !e)}
      >
        {summary}
      </SectionHeader>
      {expanded ? (
        <div className="evener-activity-section__detail">
          <dl className="evener-activity-usage">
            {usage.inputTokens !== undefined ? (
              <>
                <dt>Input</dt>
                <dd>input {usage.inputTokens}</dd>
              </>
            ) : null}
            {usage.outputTokens !== undefined ? (
              <>
                <dt>Output</dt>
                <dd>output {usage.outputTokens}</dd>
              </>
            ) : null}
            {usage.cacheReadTokens !== undefined ? (
              <>
                <dt>Cache</dt>
                <dd>cache {usage.cacheReadTokens}</dd>
              </>
            ) : null}
            {usage.totalTokens !== undefined ? (
              <>
                <dt>Total</dt>
                <dd>{usage.totalTokens}</dd>
              </>
            ) : null}
            {usage.cost !== undefined ? (
              <>
                <dt>Cost</dt>
                <dd>{usage.cost}</dd>
              </>
            ) : null}
            {usage.durationMs !== undefined ? (
              <>
                <dt>Duration</dt>
                <dd>{formatDuration(usage.durationMs)}</dd>
              </>
            ) : null}
            {usage.contextUsed !== undefined ? (
              <>
                <dt>Context used</dt>
                <dd>{usage.contextUsed.toLocaleString()}</dd>
              </>
            ) : null}
            {usage.contextWindow !== undefined ? (
              <>
                <dt>Context window</dt>
                <dd>{usage.contextWindow.toLocaleString()}</dd>
              </>
            ) : null}
            {usage.contextRemaining !== undefined ? (
              <>
                <dt>Context remaining</dt>
                <dd>{usage.contextRemaining.toLocaleString()}</dd>
              </>
            ) : null}
            {usage.contextPressure !== undefined ? (
              <>
                <dt>Context pressure</dt>
                <dd>{Math.round(usage.contextPressure * 100)}%</dd>
              </>
            ) : null}
          </dl>
        </div>
      ) : null}
    </section>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}
