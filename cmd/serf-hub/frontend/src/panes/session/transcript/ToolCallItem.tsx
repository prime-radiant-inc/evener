// ToolCallItem is the item renderer for tool-call items - the wire's
// "commandExecution" ThreadItem.type (internal/appprojector/
// appwire_projection.go). It dispatches into the tool-renderer registry
// (toolRenderers.ts) by ItemModel.toolName, which pairs a raw-output default
// descriptor (toolRenderers.ts's DEFAULT_DESCRIPTOR) with the real per-tool
// descriptors registered under tools/.
import { memo, useLayoutEffect, useState } from "react";
import { Chip } from "../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "./fileOpenBeside";
import { ImageGallery } from "./flow/ImageGallery";
import styles from "./toolcallitem.module.css";
import { toolRendererFor } from "./toolRenderers";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "./types";

const CLASS = {
  call: requireClass(styles.call, "toolcallitem.module.css", "call"),
  summary: requireClass(styles.summary, "toolcallitem.module.css", "summary"),
  error: requireClass(styles.error, "toolcallitem.module.css", "error"),
};

// A tool call failed or was denied when its ItemModel carries error text. That
// PRESENCE is the primary signal (the reducer maps ThreadItem.error straight
// through, so it survives an old-daemon reload whose settled status is still
// "completed"); the honest status "failed" the wire now stamps
// (apptranscript.SettledToolStatus, appwire_projection.go:438) is corroboration
// for the same-daemon live/reload paths. A whitespace-empty error is not a
// failure — the projector only stamps failed when data.Error != "".
function toolFailed(item: ItemRenderProps["item"]): boolean {
  return (item.error !== undefined && item.error !== "") || item.status === "failed";
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
  const failed = toolFailed(item);
  const hasErrorText = item.error !== undefined && item.error !== "";

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
  // item.id (yt2q), so it survives the VirtualList/dockview remount that would
  // reset a component-local useState. autoDefault is only the store's FALLBACK:
  // the moment the reader toggles, the store holds an explicit entry that wins.
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

  const expanded = isDisclosureOpen(item.id, autoDefault);

  // A descriptor may suppress its whole row (task_list `action:"view"` and
  // malformed non-mutations - the legacy "no card, no divider, no tool-call
  // row"). Checked AFTER the hooks above so the hook order stays stable across
  // renders; an errored call is never suppressed (its error still surfaces
  // below).
  if (descriptor.suppress?.(item)) return null;

  // A failed row is never a bare summary line even with no body/images: the
  // reader must be able to open it and read the error, so it is always a
  // <details>.
  if (!Body && !hasOutputImages && !failed) {
    return (
      <div className={CLASS.call} data-testid="tool-call-item" data-tool-name={item.toolName ?? ""}>
        <span className={CLASS.summary}>{descriptor.summary(item)}</span>
        {openBesideButton}
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
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable (implicit role="button", Enter/Space already synthesize this same click) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          // Fully controlled rather than relying on <details>' own native
          // toggle: preventDefault stops the browser from also flipping
          // `open` itself, so the disclosureStore stays the single source of
          // truth. toggleDisclosure writes an explicit store entry against
          // this id, so the user's own choice wins over autoDefault (the
          // fallback) from here on AND survives a remount (yt2q).
          e.preventDefault();
          toggleDisclosure(item.id, autoDefault);
        }}
      >
        {/* Only failure earns a glyph - a danger-toned marker, colour spent on
            the one thing that needs the eye (parity §11:261). */}
        {failed && <Chip tone="danger">Failed</Chip>}
        {descriptor.summary(item)}
        {openBesideButton}
      </summary>
      {hasErrorText && <div className={CLASS.error}>{item.error}</div>}
      {Body && <Body item={item} live={live} />}
      <ImageGallery images={item.outputImages} />
    </details>
  );
}, ignoringTurn);

registerItemRenderer("commandExecution", ToolCallItem);
