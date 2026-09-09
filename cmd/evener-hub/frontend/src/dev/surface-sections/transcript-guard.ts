import { createElement } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { TranscriptBody } from "../../panes/session/transcript/TranscriptBody";
import { makeTranscriptDisplayConfig } from "../../transcriptDisplay/config";
import "../../styles/global.css";
import { makeEditorialTranscriptModel } from "./transcript-fixture";

function required<T>(value: T | null | undefined): T {
  if (value == null) throw new Error("Missing required transcript fixture element");
  return value;
}

function visibleWithin(element: HTMLElement, box: DOMRect) {
  const rect = element.getBoundingClientRect();
  const style = getComputedStyle(element);
  return (
    rect.width > 0 &&
    rect.height > 0 &&
    style.visibility === "visible" &&
    style.display !== "none" &&
    style.opacity !== "0" &&
    rect.left >= box.left - 1 &&
    rect.right <= box.right + 1
  );
}

/** Real-component geometry fixture, imported only by the transcript browser case. */
export async function measureEditorialTranscript() {
  const host = required(document.getElementById("root"));
  const root = createRoot(host);
  const model = makeEditorialTranscriptModel();
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const results = [];
  for (const theme of ["light", "dark"]) {
    document.body.dataset.theme = theme;
    for (const scale of ["m", "xl"]) {
      document.body.dataset.fontSize = scale;
      for (const width of window.innerWidth > 899 ? [560, window.innerWidth] : [window.innerWidth]) {
        host.style.width = `${width}px`;
        flushSync(() =>
          root.render(
            createElement(TranscriptBody, {
              model,
              config,
              surface: "preview",
              disclosureScope: `editorial:${theme}:${scale}:${width}`,
              sessionRef: "dev-editorial",
            }),
          ),
        );
        await document.fonts.ready;
        const box = host.getBoundingClientRect();
        const tools = Array.from(host.querySelectorAll<HTMLElement>('[data-testid="tool-call-item"]'));
        const opens = Array.from(host.querySelectorAll<HTMLElement>('button[aria-label="Open transcript"]'));
        const lifecycle = Array.from(host.querySelectorAll<HTMLElement>('[data-testid="delegate-lifecycle"]'));
        const outside = [...tools, ...opens, ...lifecycle]
          .filter((element) => {
            const r = element.getBoundingClientRect();
            return r.left < box.left - 1 || r.right > box.right + 1;
          })
          .map((element) => element.outerHTML.slice(0, 180));
        const attention = required(lifecycle.find((element) => element.dataset.attention === "true"));
        const attentionVisible = visibleWithin(attention, box);
        const user = required(host.querySelector<HTMLElement>('[data-testid="user-bubble"]'));
        const userText = required(user.firstElementChild);
        const agent = host.querySelector<HTMLElement>('[data-testid="agent-bubble"] p');
        const quote = required(host.querySelector<HTMLElement>('[data-testid="subagent-quote"]'));
        const card = required(quote.closest<HTMLElement>('[data-testid="subagent-row"]'));
        const openControls = opens.map((button) => ({
          width: button.getBoundingClientRect().width,
          height: button.getBoundingClientRect().height,
          nested: !!button.parentElement?.closest("button"),
        }));
        // Verify lifecycle remains visible when the actual body disclosure closes.
        const attentionTool = required(attention.closest<HTMLElement>('[data-testid="tool-call-item"]'));
        const body = required(attentionTool.querySelector<HTMLElement>('[data-testid="tool-call-body"]'));
        const toggle = required(attentionTool.querySelector<HTMLButtonElement>(`button[aria-controls="${body.id}"]`));
        flushSync(() => toggle.click());
        const collapsedAttention = required(
          attentionTool.querySelector<HTMLElement>('[data-testid="delegate-lifecycle"]'),
        );
        results.push({
          theme,
          scale,
          width,
          viewport: window.innerWidth,
          outside,
          overflow: host.scrollWidth - host.clientWidth,
          toolCount: tools.length,
          openControls,
          attentionVisible,
          collapsedAttentionVisible: visibleWithin(collapsedAttention, box),
          collapsed: !attentionTool.querySelector('[data-testid="tool-call-body"]'),
          collapsedAttention: collapsedAttention.textContent,
          userFont: getComputedStyle(userText).fontFamily,
          userSize: getComputedStyle(userText).fontSize,
          agentFont: agent && getComputedStyle(agent).fontFamily,
          quoteFont: getComputedStyle(quote).fontFamily,
          cardBackground: getComputedStyle(card).backgroundColor,
          unavailable: lifecycle.filter((element) => element.textContent === "Status unavailable").length,
        });
      }
    }
  }
  return results;
}
