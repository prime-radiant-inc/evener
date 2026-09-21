import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { createElement } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { TranscriptBody } from "../../panes/session/transcript/TranscriptBody";
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
        // A delegate's status line is the card's merged stats line while the
        // body is expanded and the standalone lifecycle div once collapsed;
        // data-status-line marks both, so one selector finds the line
        // whichever surface is mounted.
        const statusLines = Array.from(host.querySelectorAll<HTMLElement>('[data-status-line="delegate"]'));
        // The status word carries its own testid on either surface.
        const statusWords = Array.from(host.querySelectorAll<HTMLElement>('[data-testid="delegate-status-word"]'));
        const outside = [...tools, ...opens, ...statusLines]
          .filter((element) => {
            const r = element.getBoundingClientRect();
            return r.left < box.left - 1 || r.right > box.right + 1;
          })
          .map((element) => element.outerHTML.slice(0, 180));
        // Anchor on the fixture-owned delegate identity rather than a status
        // kind a future case could share, so fixture growth cannot silently
        // re-target the guard. Match the identity span's exact text anywhere
        // in the card - a prefix match would also catch any future
        // dlg_editorial_running* case id, and coupling to the card's first
        // child would break on any future leading node.
        const anchorCard = required(
          Array.from(host.querySelectorAll<HTMLElement>('[data-testid="subagent-row"]')).find((card) =>
            Array.from(card.querySelectorAll<HTMLElement>("span")).some(
              (span) => span.textContent === "Delegate dlg_editorial_running",
            ),
          ),
        );
        const anchor = required(anchorCard.querySelector<HTMLElement>('[data-testid="subagent-stats"]'));
        const anchorVisible = visibleWithin(anchor, box);
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
        const anchorTool = required(anchor.closest<HTMLElement>('[data-testid="tool-call-item"]'));
        const body = required(anchorTool.querySelector<HTMLElement>('[data-testid="tool-call-body"]'));
        const toggle = required(anchorTool.querySelector<HTMLButtonElement>(`button[aria-controls="${body.id}"]`));
        flushSync(() => toggle.click());
        const collapsedStatusLine = required(
          anchorTool.querySelector<HTMLElement>('[data-testid="delegate-lifecycle"]'),
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
          anchorVisible,
          collapsedStatusVisible: visibleWithin(collapsedStatusLine, box),
          collapsed: !anchorTool.querySelector('[data-testid="tool-call-body"]'),
          collapsedStatus: collapsedStatusLine.textContent,
          userFont: getComputedStyle(userText).fontFamily,
          userSize: getComputedStyle(userText).fontSize,
          agentFont: agent && getComputedStyle(agent).fontFamily,
          quoteFont: getComputedStyle(quote).fontFamily,
          cardBackground: getComputedStyle(card).backgroundColor,
          unavailable: statusWords.filter((element) => element.textContent === "Status unavailable").length,
        });
      }
    }
  }
  return results;
}
