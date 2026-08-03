#!/usr/bin/env node
// Exact sandbox-only overflowguard adapter retained for Task 4 review.
// The fixed sandbox denies Vite's loopback listener, so this loads a temporary
// Vite production build of the same real React overflow harness through file://
// and preserves the repository runner's assertions.
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { evalInFreshChrome } from "./cdp-pipe.mjs";

const harness = process.env.OVERFLOW_HARNESS;
const measurementsFile = process.env.SERF_OVERFLOW_MEASUREMENTS;
if (!harness) throw new Error("OVERFLOW_HARNESS is required");
if (!measurementsFile) throw new Error("SERF_OVERFLOW_MEASUREMENTS is required");
const widths = process.argv.slice(2).map(Number).filter(Boolean);
if (widths.length === 0) throw new Error("at least one width is required");

const expression = `(async () => {
  await window.settled;
  const contextContract = (() => {
    const pane = document.getElementById("oh-pane");
    const statusRows = pane ? [...pane.querySelectorAll('[data-testid="status-row"]')] : [];
    const statusRow = statusRows[0] ?? null;
    const meters = statusRow ? [...statusRow.querySelectorAll("meter")] : [];
    const meter = meters[0] ?? null;
    const visual = statusRow?.querySelector('[data-testid="status-row-context"]') ?? null;
    const wide = visual?.querySelector('[data-testid="status-row-context-meter"]') ?? null;
    const compact = visual?.querySelector('[data-testid="status-row-context-percent"]') ?? null;
    const body = pane?.querySelector('[data-testid="session-chrome-body"]') ?? null;
    return {
      statusRowCount: statusRows.length,
      meterCount: meters.length,
      meterTag: meter?.tagName ?? null,
      meterValue: meter?.getAttribute("value") ?? null,
      meterMax: meter?.getAttribute("max") ?? null,
      meterLabel: meter?.getAttribute("aria-label") ?? null,
      meterParentIsStatusRow: meter?.parentElement === statusRow,
      meterNextIsVisual: meter?.nextElementSibling === visual,
      visualCount: statusRow?.querySelectorAll('[data-testid="status-row-context"]').length ?? 0,
      visualTag: visual?.tagName ?? null,
      visualAriaHidden: visual?.getAttribute("aria-hidden") ?? null,
      bodyClientWidth: body?.clientWidth ?? null,
      bodyContainerType: body ? getComputedStyle(body).containerType : null,
      wideDisplay: wide ? getComputedStyle(wide).display : null,
      compactDisplay: compact ? getComputedStyle(compact).display : null,
      compactText: compact?.textContent ?? null,
    };
  })();
  const exceptionSafety = (() => {
    const systemPrompt = [...document.querySelectorAll('[data-testid="system-notice-scaffold"]')].find((details) =>
      details.querySelector(':scope > summary')?.textContent?.startsWith('System prompt'),
    );
    const rawNotification = document.querySelector('[data-testid="notification-raw-disclosure"]');
    const details = [systemPrompt, rawNotification].filter(Boolean);
    const originalOpen = details.map((details) => details.open);
    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    let threw = false;
    HTMLElement.prototype.getBoundingClientRect = function () {
      if (this.closest('[data-testid="system-notice-scaffold"], [data-testid="notification-raw-disclosure"]')) {
        throw new Error("forced disclosure geometry failure");
      }
      return originalGetBoundingClientRect.call(this);
    };
    try {
      window.measure();
    } catch {
      threw = true;
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
    return { found: details.length, threw, originalOpen, restoredOpen: details.map((details) => details.open) };
  })();
  return { ...window.measure(), contextContract, exceptionSafety };
})()`;

let failures = 0;
for (const width of widths) {
  const url = new URL(pathToFileURL(harness));
  url.searchParams.set("w", String(width));
  const result = await evalInFreshChrome(url.href, expression);
  appendFileSync(measurementsFile, `${JSON.stringify({ width, result })}\n`);
  let widthFailed = false;
  const context = result.contextContract;
  if (
    context.statusRowCount !== 1 ||
    context.meterCount !== 1 ||
    context.meterTag !== "METER" ||
    context.meterValue !== "67345" ||
    context.meterMax !== "100001" ||
    context.meterLabel !== "Context: 67345 of 100001 tokens used, 67 percent" ||
    !context.meterParentIsStatusRow ||
    !context.meterNextIsVisual ||
    context.visualCount !== 1 ||
    context.visualTag !== "SPAN" ||
    context.visualAriaHidden !== "true"
  ) {
    widthFailed = true;
    console.log(`${width}px ... FAIL - real context structure: ${JSON.stringify(context)}`);
  } else if (context.bodyClientWidth === null || context.bodyContainerType !== "inline-size") {
    widthFailed = true;
    console.log(`${width}px ... FAIL - context container contract: ${JSON.stringify(context)}`);
  } else {
    const expectWide = context.bodyClientWidth >= 400;
    const wideShown = context.wideDisplay !== "none";
    const compactShown = context.compactDisplay !== "none";
    if (wideShown !== expectWide || compactShown === expectWide || context.compactText !== "67%") {
      widthFailed = true;
      console.log(
        `${width}px ... FAIL - context variant at ${context.bodyClientWidth}px body: ` +
          `wide=${context.wideDisplay}, compact=${context.compactDisplay}, text=${JSON.stringify(context.compactText)}, ` +
          `expected=${expectWide ? "wide" : "compact"}`,
      );
    } else {
      console.log(
        `${width}px ... context PASS - one native meter + aria-hidden sibling; ` +
          `${context.bodyClientWidth}px body shows ${expectWide ? "wide meter" : "compact percentage"}`,
      );
    }
  }
  if (!result.footer.effortVisible || !result.footer.contextVisible || !result.footer.queueVisible) {
    widthFailed = true;
    console.log(
      `${width}px ... FAIL - pressured footer facts missing: ` +
        `effort=${result.footer.effortVisible}, context=${result.footer.contextVisible}, queue=${result.footer.queueVisible}`,
    );
  }
  if (result.footer.queueLabel !== "12 queued") {
    widthFailed = true;
    console.log(`${width}px ... FAIL - pressured footer queue label is ${JSON.stringify(result.footer.queueLabel)}`);
  }
  if (result.footer.statusScrollWidth > result.footer.statusClientWidth + 1) {
    widthFailed = true;
    console.log(
      `${width}px ... FAIL - footer status facts are internally clipped: ` +
        `${result.footer.statusScrollWidth}px in ${result.footer.statusClientWidth}px`,
    );
  }
  if (result.footer.modelClientWidth <= 0) {
    widthFailed = true;
    console.log(`${width}px ... FAIL - pressured footer model has zero visible width`);
  }
  if (
    result.exceptionSafety.found !== 2 ||
    !result.exceptionSafety.threw ||
    JSON.stringify(result.exceptionSafety.originalOpen) !== JSON.stringify(result.exceptionSafety.restoredOpen)
  ) {
    widthFailed = true;
    console.log(
      `${width}px ... FAIL - disclosure exception safety: ` +
        `found=${result.exceptionSafety.found}, threw=${result.exceptionSafety.threw}, ` +
        `original=${JSON.stringify(result.exceptionSafety.originalOpen)}, ` +
        `restored=${JSON.stringify(result.exceptionSafety.restoredOpen)}`,
    );
  }
  if (result.disclosures.length !== 2) {
    widthFailed = true;
    console.log(`${width}px ... FAIL - disclosure browser contract found ${result.disclosures.length} of 2 fixtures`);
  }
  for (const disclosure of result.disclosures) {
    if (!disclosure.openDuringOverflowScan) {
      widthFailed = true;
      console.log(`${width}px ... FAIL - ${disclosure.kind} body was closed during horizontal-overflow scan`);
    }
    if (disclosure.restoredOpen !== disclosure.originalOpen) {
      widthFailed = true;
      console.log(`${width}px ... FAIL - ${disclosure.kind} disclosure state was not restored after scan`);
    }
    if (disclosure.kind === "raw-notification" && disclosure.bodyTextLength < 12000) {
      widthFailed = true;
      console.log(
        `${width}px ... FAIL - raw-notification overflow fixture body is only ${disclosure.bodyTextLength} characters`,
      );
    }
    const fullWidth =
      disclosure.summaryWidth >= disclosure.expectedWidth - 1 &&
      disclosure.bodyWidth >= disclosure.expectedWidth - 1;
    const stacked = disclosure.bodyTop >= disclosure.summaryBottom - 1;
    const aligned = Math.abs(disclosure.summaryLeft - disclosure.bodyLeft) <= 1;
    if (
      disclosure.summaryDisplay !== "list-item" ||
      disclosure.markerDisplay === "none" ||
      !fullWidth ||
      !stacked ||
      !aligned
    ) {
      widthFailed = true;
      console.log(
        `${width}px ... FAIL - ${disclosure.kind} disclosure affordance/layout: ` +
          `summary=${disclosure.summaryDisplay}, marker=${disclosure.markerDisplay}, ` +
          `summary/body=${disclosure.summaryWidth.toFixed(1)}/${disclosure.bodyWidth.toFixed(1)}px, ` +
          `expected=${disclosure.expectedWidth.toFixed(1)}px, stacked=${stacked}, aligned=${aligned}`,
      );
    }
  }
  if (result.ignored.length > 0) {
    console.log(`${width}px ... ignored ${result.ignored.length} clipped/non-scroll box(es):`);
    for (const ignored of result.ignored) console.log(`    ${ignored}`);
  }
  if (result.scrollers.length === 0) {
    if (!widthFailed)
      console.log(`${width}px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally`);
  } else {
    widthFailed = true;
    console.log(`${width}px ... FAIL - ${result.scrollers.length} horizontal scroll container(s):`);
    for (const scroller of result.scrollers) {
      console.log(
        `    ${scroller.tag}.${scroller.cls}  content ${scroller.scrollWidth}px in a ${scroller.clientWidth}px box (+${scroller.overflowPx}px)`,
      );
      for (const escapee of scroller.escapees) {
        console.log(`      escapes by ${escapee.overflowPx.toFixed(1)}px: ${escapee.tag}.${escapee.cls}`);
      }
    }
  }
  if (widthFailed) failures++;
}
process.exit(failures > 0 ? 1 : 0);
