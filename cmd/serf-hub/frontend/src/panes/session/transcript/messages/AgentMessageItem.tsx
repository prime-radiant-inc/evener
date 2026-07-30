// The agentMessage item renderer - the streaming fast path (wave-4 binding
// constraint): live text streams as PLAIN text via StreamingText (no
// markdown parsing per delta); markdown parses exactly ONCE, at settle,
// through the Markdown widget. DOMAIN FINDING from T1's live run: this
// harness routes final answers through a one-shot communicate tool, so a
// streaming window can be sub-second - the live path must look right even
// then (see AgentMessageItem.test.tsx's "rapid-settle" cases). Nothing in
// this file re-declares typography (see agentmessageitem.module.css's own
// comment) specifically so there is no gap for a live/settled mismatch to
// live in.
//
// Speaker treatment (slack-lean spec, 2026-07-29-transcript-slack-lean-
// messages.md, decisions 1-2): at exchange boundaries ONLY (opensExchange -
// the same trigger the old caption eyebrow fired on, never mid-exchange) the
// message takes the speaker header - avatar tile + "Agent" + meta (model
// label and clock time, each only when defined). The opener is a flex row
// [avatar][column(header, prose)]; mid-exchange fragments render bare (no
// avatar, no header) and align under the run via TurnBlock's content-column
// indent, so no indent is added here. The header renders in BOTH the live
// and settled branches, exactly where the eyebrow appeared, so a stream
// that starts and settles within a frame keeps the same DOM shape.

import { memo, type ReactNode } from "react";
import { Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
// Direct widget path, NOT the controller-owned widgets barrel: this pane
// must not take a dependency on the barrel's ownership boundary.
import { SpeakerAvatar } from "../../../../widgets/speakeravatar";
import { StreamingText } from "../StreamingText";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import styles from "./agentmessageitem.module.css";
import { formatClockTime } from "./format";

const CLASS = {
  message: requireClass(styles.message, "agentmessageitem.module.css", "message"),
  bubble: requireClass(styles.bubble, "agentmessageitem.module.css", "bubble"),
  continuation: requireClass(styles.continuation, "agentmessageitem.module.css", "continuation"),
  opener: requireClass(styles.opener, "agentmessageitem.module.css", "opener"),
  column: requireClass(styles.column, "agentmessageitem.module.css", "column"),
  header: requireClass(styles.header, "agentmessageitem.module.css", "header"),
  name: requireClass(styles.name, "agentmessageitem.module.css", "name"),
  meta: requireClass(styles.meta, "agentmessageitem.module.css", "meta"),
};

// The header meta is "{model label} · {clock time}", each part only when
// defined: a missing label or an absent/unparseable startedAt drops just
// that part, never leaving a dangling "·", and with neither defined there
// is no meta element at all.
function metaText(agentLabel: string | undefined, startedAt: string | undefined): string | undefined {
  const parts = [agentLabel, formatClockTime(startedAt)].filter((p): p is string => p !== undefined && p !== "");
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below), so a fresh turn object on every streaming delta targeting a
// DIFFERENT item must not re-render an already-settled agent message.
export const AgentMessageItem = memo(function AgentMessageItem({
  item,
  live,
  opensExchange,
  agentLabel,
}: ItemRenderProps) {
  const meta = opensExchange ? metaText(agentLabel, item.startedAt) : undefined;
  const speaker = opensExchange ? (
    <div className={CLASS.header} data-testid="agent-speaker-header">
      <span className={CLASS.name}>Agent</span>
      {meta !== undefined && <span className={CLASS.meta}>{meta}</span>}
    </div>
  ) : null;

  // Openers wrap in a flex row [avatar][column(header, prose)] - the avatar
  // is decorative (SpeakerAvatar is aria-hidden; the header names the
  // speaker in words). Mid-exchange items skip the row entirely and render
  // as they always have: TurnBlock's content-column indent puts them under
  // the opener's prose. EITHER WAY the prose itself sits in a bubble (the
  // chat-bubbles spec, decisions 1-3): the SAME wrapper in the live and
  // settled branches, so a stream that starts and settles within a frame
  // never changes shape - tailed toward the avatar on openers, fully
  // rounded on continuations, which have no tile to point at.
  function wrap(prose: ReactNode, liveFlag: "true" | "false") {
    const bubble = (
      <div
        className={opensExchange ? CLASS.bubble : `${CLASS.bubble} ${CLASS.continuation}`}
        data-testid="agent-bubble"
      >
        {prose}
      </div>
    );
    const root = (children: ReactNode, className: string) => (
      <div
        className={className}
        data-testid="agent-message-item"
        data-live={liveFlag}
        data-opens-exchange={opensExchange ? "true" : undefined}
      >
        {children}
      </div>
    );
    if (!opensExchange) return root(bubble, CLASS.message);
    return root(
      <>
        <SpeakerAvatar speaker="agent" />
        <div className={CLASS.column}>
          {speaker}
          {bubble}
        </div>
      </>,
      `${CLASS.message} ${CLASS.opener}`,
    );
  }

  if (live) {
    const chunks = item.pendingText;
    // Nothing streamed yet (item just started, zero deltas so far) - an
    // empty shell would only flash in and out; wait for real content,
    // mirroring RawItemView's own "no chunks yet" fallback rule.
    if (!chunks || chunks.length === 0) return null;
    return wrap(<StreamingText chunks={chunks} />, "true");
  }

  // Settled with nothing to show (an empty finalize, or reset with no
  // retry yet) removes the block entirely rather than leaving a hollow
  // one - mirrors legacy's own "empty finalize" rule (parity-m4-
  // transcript.md #6, renderer.js:2810-2816).
  if (!item.text) return null;
  return wrap(<Markdown source={item.text} />, "false");
}, ignoringTurn);

registerItemRenderer("agentMessage", AgentMessageItem);
