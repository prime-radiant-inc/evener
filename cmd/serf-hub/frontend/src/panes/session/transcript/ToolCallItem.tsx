// ToolCallItem is the item renderer for tool-call items - the wire's
// "commandExecution" ThreadItem.type (internal/appprojector/
// appwire_projection.go). It dispatches into the tool-renderer registry
// (toolRenderers.ts) by ItemModel.toolName, which pairs a raw-output default
// descriptor (toolRenderers.ts's DEFAULT_DESCRIPTOR) with the real per-tool
// descriptors registered under tools/.
import { memo, useLayoutEffect, useState } from "react";
import type { ItemModel } from "../../../protocol/model";
import { useThreadsStore } from "../../../stores/threads";
import { type CadenceState, StatusDot } from "../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "./fileOpenBeside";
import { ImageGallery } from "./flow/ImageGallery";
import { statedPurposeOf, ToolRow } from "./ToolRow";
import styles from "./toolcallitem.module.css";
import { toolCallFailed, toolRendererFor } from "./toolRenderers";
import { supersededBySuccess } from "./toolSupersession";
import { parseArgs, parseJSONObject, str } from "./tools/helpers";
import { rowFromDelegateItem } from "./tools/subagentModule";
import {
  classifyJobStatus,
  effectiveRowKind,
  itemScopeKey,
  rowKeyForDelegateItem,
  type SubagentRow,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRows,
} from "./tools/subagentModuleStore";
import { WatchedChildIndicator } from "./tools/watchedChild";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "./types";

const CLASS = {
  call: requireClass(styles.call, "toolcallitem.module.css", "call"),
  body: requireClass(styles.body, "toolcallitem.module.css", "body"),
  error: requireClass(styles.error, "toolcallitem.module.css", "error"),
};

type DelegateStatusKey = "running" | "done" | "stopped" | "failed" | "unknown";

const DELEGATE_INDICATOR_STATE: Record<DelegateStatusKey, CadenceState> = {
  running: "working",
  done: "ended",
  stopped: "ended",
  failed: "failed",
  unknown: "needs-you",
};

const DELEGATE_PURPOSE_PREVIEW_MAX = 120;

function clipDelegatePurpose(text: string, max: number): string {
  const codePoints = Array.from(text);
  return codePoints.length <= max ? text : `${codePoints.slice(0, max).join("")}…`;
}

function delegatePurposeOf(item: ItemModel): string | undefined {
  const statedPurpose = statedPurposeOf(item);
  if (statedPurpose !== undefined) return statedPurpose;

  const task = str(parseArgs(item.argumentsJSON), "task")?.replace(/\s+/g, " ").trim();
  return task === undefined || task === "" ? undefined : clipDelegatePurpose(task, DELEGATE_PURPOSE_PREVIEW_MAX);
}

function delegateStatusFromItem(item: ItemModel): DelegateStatusKey {
  const parsed = parseJSONObject(item.output);
  return classifyJobStatus(parsed === undefined ? undefined : str(parsed, "status"));
}

function delegateStatusForItem(
  item: ItemModel,
  delegateRow: SubagentRow | undefined,
  live: boolean,
): DelegateStatusKey {
  const parsedOutput = parseJSONObject(item.output);
  const hasSettledOutputStatus = parsedOutput !== undefined && str(parsedOutput, "status") !== undefined;
  if (live && !hasSettledOutputStatus) return "running";
  return delegateRow ? effectiveRowKind(delegateRow) : delegateStatusFromItem(item);
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below - the descriptor's Body only ever gets `item`/`live` too, see
// toolRenderers.ts's ToolRenderProps), so a fresh turn object on every
// streaming delta targeting a DIFFERENT item must not re-render an
// already-settled tool call.
export const ToolCallItem = memo(function ToolCallItem({ item, live, sessionRef }: ItemRenderProps) {
  const descriptor = toolRendererFor(item.toolName ?? "");
  const Body = descriptor.body;
  const isDelegate = item.toolName === "delegate";
  const delegateRows = useSubagentRows(isDelegate ? turnScopeKey(sessionRef, item.turnId) : "");
  const delegateRow = isDelegate ? delegateRows.find((row) => row.rowKey === rowKeyForDelegateItem(item)) : undefined;
  const delegateTranscriptRef = isDelegate ? str(parseJSONObject(item.output) ?? {}, "transcript_ref") : undefined;
  const delegateKind = delegateStatusForItem(item, delegateRow, live);
  const delegateStatus = isDelegate ? <StatusDot state={DELEGATE_INDICATOR_STATE[delegateKind]} /> : undefined;
  const delegateScopeKey = turnScopeKey(sessionRef, item.turnId);

  useLayoutEffect(() => {
    if (!isDelegate) return;
    const { rowKey, migrateFromRowKey, row } = rowFromDelegateItem(item);
    upsertSubagentRow(delegateScopeKey, { rowKey, ...row }, migrateFromRowKey);
  }, [delegateScopeKey, isDelegate, item]);

  // A file-referencing tool (read_file/edit_file/write_file) exposes the file it
  // touches via descriptor.openBesidePath; ToolCallItem turns that into an "open
  // beside" control in the row's summary (floor §3.7). The control itself gates
  // out-of-cwd paths (renders nothing), so this only needs the path + the ref.
  const openBesidePath = descriptor.openBesidePath?.(item);
  const openBesideButton =
    openBesidePath !== undefined && sessionRef !== undefined ? (
      <FileOpenBesideButton absPath={openBesidePath} sessionRef={sessionRef} />
    ) : null;
  // outputImages is a generic ItemModel field any tool call can carry (the
  // wire's ToolCallEndData.OutputImages, agent/events/payloads.go), not
  // owned by any one descriptor - rendered here, once, so every current and
  // future tool gets it for free rather than each body wiring it in itself.
  const hasOutputImages = (item.outputImages?.length ?? 0) > 0;
  // Two independent failure signals, OR'd: the generic wire one (error text /
  // honest status) and the descriptor's own (a shell command that ran and
  // exited nonzero is a clean tool RESULT the reader still needs marked).
  const failed = toolCallFailed(item);
  const hasErrorText = item.error !== undefined && item.error !== "";
  // Rendered in TWO places, deliberately: the collapsed row's hover title (a
  // glance) and the expanded body as real text (the keyboard-reachable copy).
  const detail = descriptor.detail?.(item);

  // summarySuffix (kata h70z) reads the FULL thread model, not just this
  // item - ask_user's "— answered: ..." recap lives in a separate, LATER
  // userMessage item. Subscribed reactively (not a one-off snapshot) so a
  // settled, already-collapsed row's summary updates the moment that later
  // reply lands, even though this memoized component would otherwise bail
  // on unchanged item/live/sessionRef props.
  const summarySuffix = useThreadsStore((s) =>
    descriptor.summarySuffix?.(item, sessionRef !== undefined ? s.threads.get(sessionRef) : undefined),
  );
  const summary = descriptor.summary(item) + (summarySuffix ?? "");
  const purpose = isDelegate ? delegatePurposeOf(item) : item.description;
  // kata xw3t: the URL, if any, embedded in this row's own summary text -
  // web_fetch's only descriptor with one today. Read directly off the item
  // (not the thread model): unlike summarySuffix, nothing about which URL a
  // call's own summary names can change after the call settles.
  const summaryLink = descriptor.summaryLink?.(item);

  // Every row with a body starts collapsed (parity-m4-transcript.md's own
  // Highlights: "every tool row, including diffs, starts collapsed" - the
  // only default-expanded state anywhere is a failed call once it settles,
  // via descriptor.autoExpand OR a tool error/denial (item.error, parity §11:
  // "only failure earns the eye"; §2:100's force-open on error)). autoExpand
  // only means anything once the call has actually finished (e.g. shell's own
  // exit-code heuristic can't resolve mid-stream), so it is consulted exactly
  // once, at the live -> settled transition, and stashed as autoDefault -
  // never re-consulted on every render (both to honor that "once" contract and
  // so a settled row's later re-renders never re-fight the reader's toggle).
  //
  // The open/closed state itself lives in the shared disclosureStore keyed by
  // session ref plus item id, so it survives the VirtualList/dockview remount
  // without colliding with identical item ids in another session.
  // autoDefault is only the store's FALLBACK: the moment the reader toggles,
  // the store holds an explicit entry that wins.
  const [autoDefault, setAutoDefault] = useState(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: deliberately edge-triggered on live only, see the comment inside
  useLayoutEffect(() => {
    if (live) return;
    setAutoDefault((descriptor.autoExpand?.(item) ?? false) || failed);
    // Edge-triggered on the live -> settled transition (and on an
    // already-settled initial mount) - deliberately NOT depending on
    // `item`/`descriptor`/`failed` too, so a settled row's later re-renders
    // never re-run this and re-fight a manual toggle.
  }, [live]);

  // kata hgm1: "only failure earns the eye" stays the rule for every real
  // execution failure/denial, unchanged. The one carve-out is a preval-only
  // bounce (item.prevalOnly - never reached the tool's real execution)
  // whose very next same-tool call went on to succeed: the model corrected
  // itself, so the failure that force-opens by default demotes to the same
  // fallback a clean call gets. It stays fully attributable (failed/
  // data-failed/the error text itself are untouched, see below) - only the
  // default OPEN state changes. Read reactively off the live thread model
  // (like summarySuffix above) rather than folded into autoDefault's own
  // edge-triggered effect, so a row that settled BEFORE its correction
  // landed still collapses the moment it does - autoDefault itself is only
  // ever a fallback, so recomputing what it feeds into here never re-fights
  // an explicit reader toggle (disclosureStore's own contract).
  const superseded = useThreadsStore((s) =>
    supersededBySuccess(item, sessionRef !== undefined ? s.threads.get(sessionRef) : undefined),
  );
  const disclosureKey = itemScopeKey(sessionRef, item.id);
  const expanded = isDisclosureOpen(disclosureKey, autoDefault && !superseded);

  // A descriptor may suppress its whole row (task_list `action:"view"` and
  // malformed non-mutations - the legacy "no card, no divider, no tool-call
  // row"). Checked AFTER the hooks above so the hook order stays stable across
  // renders; an errored call is never suppressed (its error still surfaces
  // below).
  if (descriptor.suppress?.(item)) return null;

  // The module's rich watcher only exists while the body is expanded. Keep a
  // single lean watcher mounted for the collapsed state so the top-level dot
  // still follows terminal child status; the rich body watcher takes over
  // when the disclosure opens.
  const leanDelegateWatch =
    isDelegate && !expanded && delegateTranscriptRef ? (
      <WatchedChildIndicator
        ref={delegateTranscriptRef}
        scopeKey={turnScopeKey(sessionRef, item.turnId)}
        rowKey={rowKeyForDelegateItem(item)}
        renderCadence={false}
      />
    ) : null;

  // A failed row is never a bare summary line even with no body/images: the
  // reader must be able to open it and read the error, so it is always a
  // <details>.
  if (!Body && !hasOutputImages && !failed) {
    return (
      <div className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""}>
        <ToolRow
          summary={isDelegate ? "" : summary}
          summaryLink={summaryLink}
          purpose={purpose}
          icon={descriptor.icon}
          monoSummary={descriptor.monoSummary}
          failed={false}
          status={delegateStatus}
          expandable={false}
          expanded={false}
          trailing={openBesideButton}
          title={detail}
        />
      </div>
    );
  }

  return (
    <details
      className={CLASS.call}
      data-testid="tool-call-item"
      data-tool-name={item.toolName ?? ""}
      // A failed row carries data-attention="error" for the same urgent-anchor
      // search the legacy shell tagged failed rows with (parity §11's
      // dataset.attention="error"); a clean row carries neither attribute so it
      // recedes (success is glyph-less).
      data-failed={failed ? "true" : undefined}
      data-attention={failed ? "error" : undefined}
      open={expanded}
    >
      <ToolRow
        // A descriptor whose summary duplicates what its expanded body shows
        // (shell: the raw one-line command vs the body's pretty-printed
        // block) drops the summary line while open - the body is the single
        // representation. Collapsed, the summary stays: it is the only glance.
        summary={isDelegate || (expanded && descriptor.summaryHiddenWhenExpanded) ? "" : summary}
        summaryLink={summaryLink}
        purpose={purpose}
        icon={descriptor.icon}
        monoSummary={descriptor.monoSummary}
        failed={failed}
        status={delegateStatus}
        expandable
        expanded={expanded}
        // toggleDisclosure writes an explicit store entry against this
        // session-scoped item key, so the user's own choice wins over
        // autoDefault (the fallback) from here on and survives a remount.
        onToggle={() => toggleDisclosure(disclosureKey, autoDefault && !superseded)}
        trailing={openBesideButton}
        title={detail}
      />
      {/* The expanded content is one wrapper, so the open transition (A6) and
          the row-to-body spacing live in one rule rather than per-descriptor.
          Rendered only when open: an unmounted body can animate in on the next
          open, and a collapsed row costs nothing to render. */}
      {leanDelegateWatch}
      {expanded && (
        <div className={CLASS.body} data-testid="tool-call-body">
          {/* descriptor.detail() (currently only shell's exit code) rides the
              collapsed row's hover title ONLY (see `title={detail}` above) - it
              is not echoed here as a second copy. A title alone is mouse-only,
              but for shell that is not a reachability gap: the daemon bakes the
              same "[exit N]" fact into the captured output itself
              (agent/session_tools_shell.go's formatShellResult - the model
              reads that same text as its tool result), so it is already real,
              keyboard/screen-reader-reachable text at the tail of the body
              below. Echoing detail() here too duplicated that fact on screen
              (kata wksf) instead of adding a second way to reach it. */}
          {hasErrorText && <div className={CLASS.error}>{item.error}</div>}
          {Body && <Body item={item} live={live} sessionRef={sessionRef} />}
          <ImageGallery images={item.outputImages} />
        </div>
      )}
    </details>
  );
}, ignoringTurn);

registerItemRenderer("commandExecution", ToolCallItem);
