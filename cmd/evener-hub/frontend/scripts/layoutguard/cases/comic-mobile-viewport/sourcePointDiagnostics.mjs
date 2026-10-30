import { evaluate } from "../../../browserGuardCdp.mjs";

// Only source identifiers and numbers cross the diagnostic boundary. Never
// include transcript/draft text, arbitrary stored fields, or exception details.
function summarizePosition(position) {
  if (!position) return null;
  const clip = (value) => typeof value === "string" ? value.slice(0, 96) : null;
  const number = (value) => Number.isFinite(value) ? value : null;
  const path = Array.isArray(position.path) ? position.path : null;
  return {
    mode: clip(position.mode), sessionRef: clip(position.sessionRef), entryId: clip(position.entryId),
    path: path?.slice(0, 8).map(clip) ?? null,
    offset: number(position.offset), viewportOffset: number(position.viewportOffset),
    truncated: path?.length > 8 || [position.mode, position.sessionRef, position.entryId, ...path ?? []]
      .some((value) => value?.length > 96),
  };
}

// This is a snapshot AFTER the failed wait, not a second assertion or a record
// of the final poll. Read current storage and the Range in one page evaluation.
function sourcePointSnapshot(expected, summarize) {
  const saved = JSON.parse(sessionStorage.getItem("evener:comic:position:local:comic-geometry") ?? "null")?.position;
  const port = document.querySelector("[data-comic-scroll]")?.firstElementChild;
  const mounted = [...port?.querySelectorAll("[data-comic-entry]") ?? []];
  const matches = (node, position) => !!position &&
    node.dataset.comicSession === position.sessionRef && node.dataset.comicEntry === position.entryId &&
    node.dataset.comicPath === JSON.stringify(position.path) &&
    +node.dataset.comicStart <= position.offset && +node.dataset.comicEnd > position.offset;
  const source = mounted.find((node) => matches(node, saved));
  let witness = null;
  if (source) {
    const start = +source.dataset.comicStart, end = +source.dataset.comicEnd;
    const text = source.querySelector("[data-comic-text]");
    witness = {
      start, end, textPresent: !!text,
      literal: text ? text.textContent === window.comicGeometry.source.slice(start, end) : null,
      y: null, delta: null,
    };
    if (text) {
      const walker = document.createTreeWalker(text, NodeFilter.SHOW_TEXT);
      let offset = saved.offset - start, node;
      while ((node = walker.nextNode())) {
        if (offset < node.length) {
          const range = document.createRange();
          range.setStart(node, offset);
          range.setEnd(node, offset + 1);
          witness.y = range.getBoundingClientRect().top - port.getBoundingClientRect().top;
          witness.delta = Number.isFinite(saved.viewportOffset) ? witness.y - saved.viewportOffset : null;
          break;
        }
        offset -= node.length;
      }
    }
  }
  return {
    font: document.querySelector("[data-comic-font]")?.dataset.comicFont?.slice(0, 96) ?? null,
    current: summarize(saved), scrollTop: port?.scrollTop ?? null,
    mounted: {
      count: mounted.length, truncated: mounted.length > 16,
      ranges: mounted.slice(0, 16).map((node) => ({
        start: +node.dataset.comicStart, end: +node.dataset.comicEnd,
        matchesExpected: matches(node, expected), matchesCurrent: matches(node, saved),
      })),
    },
    source: witness,
  };
}

export async function appendSourcePointDiagnostic(error, send, expected, stage) {
  try {
    const diagnostic = { ...stage, expected: summarizePosition(expected), capture: "unavailable" };
    try {
      Object.assign(diagnostic, await evaluate(send,
        `(${sourcePointSnapshot})(${JSON.stringify(expected)}, ${summarizePosition})`), { capture: "collected" });
    } catch {
      // A failed page/transport read is metadata, never the primary failure.
    }
    error.sourcePointDiagnostic = diagnostic;
    // The existing layoutguard reporter uses error.message, not custom fields.
    error.message += `\nComic source-point evidence: ${JSON.stringify(diagnostic)}`;
  } catch {
    // Even failure to attach evidence must leave the original error primary.
  }
}
