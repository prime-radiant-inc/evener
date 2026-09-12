// ToolCallItem is the item renderer for tool-call items - the wire's
// "commandExecution" ThreadItem.type (internal/appprojector/
// appwire_projection.go). It dispatches into the tool-renderer registry
// (toolRenderers.ts) by ItemModel.toolName, which pairs a raw-output default
// descriptor (toolRenderers.ts's DEFAULT_DESCRIPTOR) with the real per-tool
// descriptors registered under tools/.
import { memo, useId, useLayoutEffect, useState } from "react";
import type { ItemModel, ThreadModel } from "../../../protocol/model";
import { stableDelegateDisplayStatus } from "../../../protocol/stableDelegate";
import { useThreadsStore } from "../../../stores/threads";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  summaryOpenByDefault,
  type TranscriptRenderContextValue,
  useTranscriptRenderContext,
} from "../../../transcriptDisplay/renderContext";
import { type CadenceState, StatusDot } from "../../../widgets";
import {
  disclosureDefault,
  isDisclosureOpen,
  scopedDisclosureId,
  toggleDisclosure,
} from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton, fileDocParams } from "./fileOpenBeside";
import { ImageGallery } from "./flow/ImageGallery";
import { OpenTranscriptButton } from "./openTranscript";
import { statedIntentOf, ToolRow } from "./ToolRow";
import styles from "./toolcallitem.module.css";
import { toolCallFailed, toolRendererFor } from "./toolRenderers";
import { supersededBySuccess } from "./toolSupersession";
import { parseArgs, parseJSONObject, str } from "./tools/helpers";
import { rowFromDelegateItem } from "./tools/subagentModule";
import {
  effectiveRowKind,
  removeSubagentRow,
  rowKeyForDelegateItem,
  turnScopeKey,
  upsertSubagentRow,
} from "./tools/subagentModuleStore";
import delegateStyles from "./tools/subagentmodule.module.css";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "./types";

const CLASS = {
  call: requireClass(styles.call, "toolcallitem.module.css", "call"),
  body: requireClass(styles.body, "toolcallitem.module.css", "body"),
  error: requireClass(styles.error, "toolcallitem.module.css", "error"),
  lifecycle: requireClass(delegateStyles.lifecycle, "subagentmodule.module.css", "lifecycle"),
};

type DelegateStatusKey = "running" | "done" | "stopped" | "failed" | "unknown";

const DELEGATE_INDICATOR_STATE: Record<DelegateStatusKey, CadenceState> = {
  running: "working",
  done: "ended",
  stopped: "ended",
  failed: "failed",
  unknown: "idle",
};

const DELEGATE_LABEL: Record<DelegateStatusKey, string> = {
  running: "Running",
  done: "Idle · reported",
  stopped: "Stopped",
  failed: "Failed",
  unknown: "Status unavailable",
};

const DELEGATE_INTENT_PREVIEW_MAX = 120;

function clipDelegateIntent(text: string, max: number): string {
  const codePoints = Array.from(text);
  return codePoints.length <= max ? text : `${codePoints.slice(0, max).join("")}…`;
}

function delegateIntentOf(item: ItemModel): string | undefined {
  const statedIntent = statedIntentOf(item);
  if (statedIntent !== undefined) return statedIntent;

  const args = parseArgs(item.argumentsJSON);
  // Transcripts recorded before the rename carry the brief under `task`.
  const brief = (str(args, "prompt") ?? str(args, "task"))?.replace(/\s+/g, " ").trim();
  return brief === undefined || brief === "" ? undefined : clipDelegateIntent(brief, DELEGATE_INTENT_PREVIEW_MAX);
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below - the descriptor's Body only ever gets `item`/`live` too, see
// toolRenderers.ts's ToolRenderProps), so a fresh turn object on every
// streaming delta targeting a DIFFERENT item must not re-render an
// already-settled tool call.
interface ToolCallItemBodyProps extends ItemRenderProps {
  renderContext: TranscriptRenderContextValue;
  thread?: ThreadModel;
}

function ToolCallItemBody({ item, live, sessionRef, projectedSummary, renderContext, thread }: ToolCallItemBodyProps) {
  const context = renderContext;
  const { config } = context;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  const descriptor = toolRendererFor(item.toolName ?? "");
  const Body = descriptor.body;
  const isDelegate = item.toolName === "delegate";
  const delegateOutput = isDelegate ? parseJSONObject(item.output) : undefined;
  const stableDelegateId = delegateOutput ? str(delegateOutput, "delegate_id") : undefined;
  const stableDelegate = thread?.delegates?.find((delegate) => {
    if (sessionRef === undefined || stableDelegateId === undefined) return false;
    return delegate.delegateId === stableDelegateId;
  });
  const delegateKind = effectiveRowKind({ launching: live || item.status === "inProgress" }, stableDelegate);
  const delegateStatus =
    isDelegate && delegateKind !== "unknown" ? <StatusDot state={DELEGATE_INDICATOR_STATE[delegateKind]} /> : undefined;
  const lifecycleStatus = stableDelegate ? stableDelegateDisplayStatus(stableDelegate) : undefined;
  const lifecycle = isDelegate ? (
    <div
      className={CLASS.lifecycle}
      data-testid="delegate-lifecycle"
      data-kind={delegateKind}
      data-attention={stableDelegate?.needsAttention ? "true" : undefined}
    >
      {lifecycleStatus === "exhausted"
        ? "Exhausted"
        : lifecycleStatus === "idle"
          ? "Idle"
          : DELEGATE_LABEL[delegateKind]}
      {stableDelegate?.needsAttention && <span>◆ Needs attention</span>}
    </div>
  ) : null;
  const delegateScopeKey = turnScopeKey(sessionRef, item.turnId);

  useLayoutEffect(() => {
    if (!isDelegate) return;
    const projected = rowFromDelegateItem(item, live);
    if (!projected) {
      removeSubagentRow(delegateScopeKey, rowKeyForDelegateItem(item));
      return;
    }
    const { rowKey, migrateFromRowKey, row } = projected;
    upsertSubagentRow(delegateScopeKey, { rowKey, ...row }, migrateFromRowKey);
  }, [delegateScopeKey, isDelegate, item, live]);

  // A file-referencing tool (read_file/edit_file/write_file) exposes the file it
  // touches via descriptor.openBesidePath; ToolCallItem turns that into an "open
  // beside" control in the row's summary (floor §3.7). fileDocParams (the same
  // presence check FileOpenBesideButton applies internally, kept there too as
  // defense-in-depth) is hoisted HERE: ToolRow must never see a truthy
  // trailing/trailingAfter for a path the button will end up rendering nothing
  // for, or it builds a dead anchor-split wrapper for every out-of-cwd row
  // (kata ledger #96).
  const openBesidePath = descriptor.openBesidePath?.(item);
  // cwd is snapshot-only ThreadModel state (fileOpenBeside.tsx's DECISION B),
  // stable for the pane's life. ONE by-ref subscription serves both readers:
  // the open-beside presence check just below, and summary()'s
  // ToolSummaryContext further down, which shell's own descriptor uses to
  // strip a redundant "cd <cwd> && " prefix from its summary.
  const cwd = thread?.cwd;
  const canOpenBeside = fileDocParams(openBesidePath, sessionRef, cwd) !== undefined;
  // The openBesidePath re-check is what fileDocParams already required to
  // return a value; stating it here narrows the type instead of asserting it,
  // so the button's absPath needs no cast.
  const openBesideButton =
    canOpenBeside && openBesidePath !== undefined && sessionRef !== undefined ? (
      <FileOpenBesideButton absPath={openBesidePath} sessionRef={sessionRef} cwd={cwd} />
    ) : null;
  // read_file (openBesideInline) quotes its path verbatim inside the summary,
  // so the control rides INLINE between the file name and the line range
  // rather than at the end of the line; openBesideInline hands ToolRow the
  // complete summary prefix through the anchor (not a bare path fragment -
  // ToolRow verifies it with startsWith, never searches, kata ledger #97).
  // ToolRow falls back to the end placement when the value isn't a literal
  // prefix of the summary.
  const trailingAfterBase = canOpenBeside ? descriptor.openBesideInline?.(item) : undefined;
  // A child-targeting tool (delegate_send today) exposes its target's
  // transcript ref via descriptor.openTranscriptRef; ToolCallItem turns that
  // into the same "open ⤢" control delegate rows use, riding the
  // row's trailing slot beside any file open-beside button. parentRef is the
  // enclosing session so the opened pane keeps its way back (kata 0pzz).
  const openTranscriptRef = descriptor.openTranscriptRef?.(item);
  const openTranscriptButton =
    openTranscriptRef !== undefined ? (
      <OpenTranscriptButton transcriptRef={openTranscriptRef} parentRef={sessionRef} />
    ) : null;
  // The summary quotes the delegate target verbatim before the status meta
  // ("Sent a message to delegate <id> · <status>"), so the control rides
  // INLINE between the delegate it opens and the running-state words via the
  // descriptor's openTranscriptInline anchor (the trailingAfter mechanism
  // read_file's openBesideInline uses) - never off after the status meta.
  // Gated on a defined ref like trailingAfterBase's own canOpenBeside gate:
  // ToolRow must never see a truthy trailingAfter for a button that will
  // render nothing.
  const trailingAfter =
    trailingAfterBase ?? (openTranscriptRef !== undefined ? descriptor.openTranscriptInline?.(item) : undefined);
  const trailingControls =
    openBesideButton !== null || openTranscriptButton !== null ? (
      <>
        {openBesideButton}
        {openTranscriptButton}
      </>
    ) : null;
  // outputImages is a generic ItemModel field any tool call can carry (the
  // wire's ToolCallEndData.OutputImages, agent/events/payloads.go), not
  // owned by any one descriptor - rendered here, once, so every tool gets
  // it for free. A descriptor may still set HOW large they render
  // (outputImageSize).
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
  const summarySuffix = descriptor.summarySuffix?.(item, thread);
  // cwd (subscribed once above) is threaded into summary() as
  // ToolSummaryContext so shell's own descriptor can strip a redundant
  // "cd <cwd> && " prefix from its summary.
  const statedIntent = statedIntentOf(item);
  // The descriptor's own summary (read_file's "Read <path> · lines N-M", shell's
  // "Ran <cmd>", …) is the row's summary at every verbosity level. The projected
  // summary is only a fallback for a descriptor that renders nothing — never a
  // replacement for a real one. Overriding a real summary with the projected
  // neutral text is what showed "Action summary unavailable" for every
  // intent-less tool call, including read_file rows that clearly read a file.
  const descriptorSummary = descriptor.summary(item, { cwd }) + (summarySuffix ?? "");
  const useProjectedSummary = projectedSummary !== undefined && descriptorSummary.trim() === "";
  let intent = item.description;
  if (isDelegate) {
    intent = delegateIntentOf(item);
  } else if (projectedSummary !== undefined && statedIntent !== undefined) {
    intent = projectedSummary;
  }
  // kata xw3t: the URL, if any, embedded in this row's own summary text -
  // web_fetch's only descriptor with one today. Read directly off the item
  // (not the thread model): unlike summarySuffix, nothing about which URL a
  // call's own summary names can change after the call settles.
  const summaryLink = descriptor.summaryLink?.(item);

  // Every row with a body starts collapsed (parity-m4-transcript.md's own
  // Highlights: "every tool row, including diffs, starts collapsed" - the
  // only default-expanded states are descriptor.autoExpand (a failed shell
  // call once it settles, and an image read whose picture IS its output) OR
  // a tool error/denial (item.error, parity §11: "only failure earns the
  // eye"; §2:100's force-open on error)). autoExpand only means anything once
  // the call has actually finished (e.g. shell's own exit-code heuristic
  // can't resolve mid-stream), so it is consulted exactly once, at the live
  // -> settled transition, and stashed as autoDefault - never re-consulted
  // on every render (both to honor that "once" contract and so a settled
  // row's later re-renders never re-fight the reader's toggle).
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
  const superseded = supersededBySuccess(item, thread);
  const disclosureKey = scopedDisclosureId(disclosureScope, item.id);
  const bodyId = useId();
  const configDefault = expandDetailsByDefault(config) || disclosureDefault(disclosureScope, item.id, false);
  const disclosureFallback = configDefault || (autoDefault && !superseded);
  const expanded = isDisclosureOpen(disclosureKey, disclosureFallback);

  // A descriptor whose summary duplicates what its expanded body shows
  // (shell: the raw one-line command vs the body's pretty-printed block)
  // swaps its summary text for a placeholder while the row is open. The
  // summary line itself stays - hiding it lifted the disclosure chevron off
  // the line it rides (onto the intent line, or adrift on an intent-less
  // row) - and the swap keeps the call from appearing twice. The placeholder
  // outranks the projected fallback the same way the descriptor's own summary
  // does: a real line about the call beats a neutral "unavailable" one
  // while the open body below shows the command whole.
  const expandedSummary = expanded ? descriptor.summaryWhenExpanded : undefined;
  const summary =
    expandedSummary !== undefined ? expandedSummary : useProjectedSummary ? projectedSummary : descriptorSummary;

  // Two-level disclosure: the summary line has its own open/closed state,
  // independent of the body disclosure. At verbosity levels where toolCalls is
  // true (tools/activity/full) the summary defaults open; at chat/intent it
  // defaults closed, showing only the intent. An intent-less row has no
  // separate intent line to toggle, so its summary is forced open regardless
  // of the config default. An explicit user choice (open or close) persists
  // across verbosity level changes — the default only applies when there is
  // no explicit choice.
  const summaryDisclosureKey = scopedDisclosureId(disclosureScope, `summary:${item.id}`);
  const summaryConfigDefault = summaryOpenByDefault(config);
  const summaryDisclosureOpen = isDisclosureOpen(summaryDisclosureKey, summaryConfigDefault);
  const summaryOpen = statedIntent === undefined ? true : summaryDisclosureOpen;
  // A descriptor may suppress its whole row (task_list `action:"view"` and
  // malformed non-mutations - the legacy "no card, no divider, no tool-call
  // row"). Checked AFTER the hooks above so the hook order stays stable across
  // renders; an errored call is never suppressed (its error still surfaces
  // below).
  if (descriptor.suppress?.(item)) return null;

  // A failed row is never a bare summary line even with no body/images: the
  // reader must be able to open it and read the error, so it is always an
  // expandable disclosure. A descriptor may also report per-item that its
  // body renders nothing (hasBody) — a summary-only rendering offers no
  // disclosure that would open to nothing.
  if ((!Body || descriptor.hasBody?.(item) === false) && !hasOutputImages && !failed) {
    return (
      <div className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""}>
        <ToolRow
          summary={isDelegate ? "" : summary}
          summaryLink={summaryLink}
          intent={intent}
          icon={descriptor.icon}
          monoSummary={descriptor.monoSummary}
          failed={false}
          status={delegateStatus}
          expandable={false}
          expanded={false}
          trailing={trailingControls}
          trailingAfter={trailingAfter}
          title={detail}
        />
        {lifecycle}
      </div>
    );
  }

  return (
    // A <div>, not a native <details>: ToolRow's expandable branch is a real
    // button with aria-expanded, and the body below is a sibling div rendered
    // conditionally on `expanded`. The native <details>/<summary> pair was
    // replaced to stop Chrome's a11y console flagging the interactive elements
    // (linkified summary, "Open beside" / "Open transcript" buttons) that
    // previously rode inline inside the disclosure trigger. Open/closed state
    // is fully controlled from disclosureStore, so a plain div wrapper is all
    // the structure that remains.
    <div
      className={CLASS.call}
      data-testid="tool-call-item"
      data-tool-name={item.toolName ?? ""}
      // A failed row carries data-attention="error" for the same urgent-anchor
      // search the legacy shell tagged failed rows with (parity §11's
      // dataset.attention="error"); a clean row carries neither attribute so it
      // recedes (success is glyph-less).
      data-failed={failed ? "true" : undefined}
      data-attention={failed ? "error" : undefined}
    >
      <ToolRow
        // The summary text is the descriptor's own - or its expanded
        // placeholder (expandedSummary above). The empty-string gate below
        // serves the remaining no-summary states: delegate rows
        // (subagentModule owns their presentation) and a two-level row whose
        // summary line the reader collapsed (summaryOpen=false).
        summary={isDelegate || !summaryOpen ? "" : summary}
        summaryLink={summaryLink}
        intent={intent}
        icon={descriptor.icon}
        monoSummary={descriptor.monoSummary}
        failed={failed}
        status={delegateStatus}
        expandable
        expanded={expanded}
        // toggleDisclosure writes an explicit store entry against this
        // session-scoped item key, so the user's own choice wins over
        // autoDefault (the fallback) from here on and survives a remount.
        onToggle={() => toggleDisclosure(disclosureKey, disclosureFallback)}
        summaryOpen={summaryOpen}
        onToggleSummary={() => toggleDisclosure(summaryDisclosureKey, summaryConfigDefault)}
        trailing={trailingControls}
        trailingAfter={trailingAfter}
        title={detail}
        bodyId={bodyId}
      />
      {lifecycle}
      {/* The expanded content is one wrapper, so the open transition (A6) and
          the row-to-body spacing live in one rule rather than per-descriptor.
          Rendered only when open: an unmounted body can animate in on the next
          open, and a collapsed row costs nothing to render. */}
      {expanded && (
        <div id={bodyId} className={CLASS.body} data-testid="tool-call-body">
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
          {Body && <Body item={item} live={live} sessionRef={sessionRef} cwd={cwd} />}
          <ImageGallery images={item.outputImages} size={descriptor.outputImageSize} />
        </div>
      )}
    </div>
  );
}

function ProviderToolCallItem(props: ItemRenderProps) {
  const context = props.renderContext;
  if (context === undefined) throw new Error("provider-backed ToolCallItem requires render context");
  return <ToolCallItemBody {...props} renderContext={context} thread={props.thread} />;
}

function LegacyToolCallItem(props: ItemRenderProps) {
  const context = useTranscriptRenderContext();
  const thread = useThreadsStore((state) => state.threads.get(props.sessionRef ?? ""));
  return <ToolCallItemBody {...props} renderContext={context} thread={thread} />;
}

export const ToolCallItem = memo(function ToolCallItem(props: ItemRenderProps) {
  return props.renderContext === undefined ? <LegacyToolCallItem {...props} /> : <ProviderToolCallItem {...props} />;
}, ignoringTurn);

registerItemRenderer("commandExecution", ToolCallItem);
