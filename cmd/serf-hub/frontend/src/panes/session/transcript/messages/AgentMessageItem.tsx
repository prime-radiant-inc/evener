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
import { registerItemRenderer, type ItemRenderProps } from "../types";
import { StreamingText } from "../StreamingText";
import { Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./agentmessageitem.module.css";

const CLASS = {
  message: requireClass(styles.message, "agentmessageitem.module.css", "message"),
};

export function AgentMessageItem({ item, live }: ItemRenderProps) {
  if (live) {
    const chunks = item.pendingText;
    // Nothing streamed yet (item just started, zero deltas so far) - an
    // empty shell would only flash in and out; wait for real content,
    // mirroring RawItemView's own "no chunks yet" fallback rule.
    if (!chunks || chunks.length === 0) return null;
    return (
      <div className={CLASS.message} data-testid="agent-message-item" data-live="true">
        <StreamingText chunks={chunks} />
      </div>
    );
  }

  // Settled with nothing to show (an empty finalize, or reset with no
  // retry yet) removes the block entirely rather than leaving a hollow
  // one - mirrors legacy's own "empty finalize" rule (parity-m4-
  // transcript.md #6, renderer.js:2810-2816).
  if (!item.text) return null;
  return (
    <div className={CLASS.message} data-testid="agent-message-item" data-live="false">
      <Markdown source={item.text} />
    </div>
  );
}

registerItemRenderer("agentMessage", AgentMessageItem);
