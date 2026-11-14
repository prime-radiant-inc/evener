// The find bar: a field, where the reader is among the matches, a step to the
// older and to the newer match, and a way out. It docks above the transcript,
// clear of the composer, so on a phone the on-screen keyboard (which lifts the
// composer, shell/useKeyboardInset.ts) never puts it over the send control.
// useFindInSession owns the search; this only shows it.
import { type KeyboardEvent, useEffect, useRef } from "react";
import { isIMECompositionKeydown } from "../../../keybindings/dispatcher";
import { Chevron, CloseIcon, IconButton, InlineNotice, Input, VisuallyHidden } from "../../../widgets";
import styles from "./findbar.module.css";
import type { FindView } from "./useFindInSession";

function statusText(view: FindView): string {
  switch (view.status) {
    case "idle":
      return "";
    case "searching":
      return "Searching older…";
    case "incomplete":
      return "Search incomplete";
    case "none":
      return "No matches";
    case "unsearched":
      return "No matches yet";
    case "matches": {
      const count = view.moreHistory ? `${view.count}+` : `${view.count}`;
      return view.position === 0 ? `${count} found` : `${view.position} of ${count}`;
    }
  }
}

// What a screen reader hears: the same answer, with the "+" spelled out.
function statusAnnouncement(view: FindView): string {
  if (view.status === "unsearched") return "No matches in loaded history. Older history has not been searched.";
  if (view.status !== "matches") return statusText(view);
  const where = view.position === 0 ? `${view.count} matches` : `Match ${view.position} of ${view.count}`;
  return view.moreHistory ? `${where}. Older history has not been searched.` : where;
}

export function FindBar({ view }: { view: FindView }) {
  const root = useRef<HTMLDivElement>(null);
  // The widget owns its input element; focus reaches it through the bar, as
  // the pane scaffold's own focus selector does for the composer.
  // biome-ignore lint/correctness/useExhaustiveDependencies: each focus request is one event
  useEffect(() => {
    const input = root.current?.querySelector("input");
    input?.focus();
    input?.select();
  }, [view.focusEpoch]);

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (isIMECompositionKeydown(event.nativeEvent)) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      view.close();
      return;
    }
    // By tag, not class: in a popped-out window the field is not an instance
    // of this window's HTMLInputElement.
    if (event.key !== "Enter" || (event.target as Element).tagName !== "INPUT") return;
    event.preventDefault();
    // Enter walks back through the session, as a reader at the live end
    // expects; Shift+Enter comes forward again.
    view.step(event.shiftKey ? 1 : -1);
  }

  // At the oldest loaded match the same step reads further back.
  const atLoadedStart = view.status === "unsearched" || (view.count > 0 && view.position === view.count);
  const olderLabel = view.moreHistory && atLoadedStart ? "Search older history" : "Older match";
  return (
    <div className={styles.dock}>
      {/* A div carries the landmark role: the <search> element is too new for
          every browser the app serves. Its keys belong to the field inside. */}
      {/* biome-ignore lint/a11y/useSemanticElements: see above */}
      <div
        ref={root}
        className={styles.bar}
        role="search"
        aria-label="Find in session"
        data-find-bar=""
        data-testid="find-bar"
        data-find-status={view.status}
        data-find-position={view.position}
        data-find-count={view.count}
        data-find-more={view.moreHistory ? "true" : "false"}
        onKeyDown={onKeyDown}
      >
        <div className={styles.field}>
          <Input
            type="search"
            value={view.query}
            onChange={(event) => view.setQuery(event.target.value)}
            placeholder="Find in session"
            aria-label="Find in session"
            autoComplete="off"
          />
        </div>
        <span className={styles.status} data-testid="find-status" aria-hidden="true">
          {statusText(view)}
        </span>
        <span role="status" aria-live="polite">
          <VisuallyHidden>{statusAnnouncement(view)}</VisuallyHidden>
        </span>
        <IconButton
          label={olderLabel}
          icon={<Chevron direction="up" />}
          variant="quiet"
          size="sm"
          disabled={!view.canStepOlder}
          onClick={() => view.step(-1)}
          data-testid="find-older"
        />
        <IconButton
          label="Newer match"
          icon={<Chevron direction="down" />}
          variant="quiet"
          size="sm"
          disabled={!view.canStepNewer}
          onClick={() => view.step(1)}
          data-testid="find-newer"
        />
        <IconButton label="Close find" icon={<CloseIcon />} variant="quiet" size="sm" onClick={view.close} />
      </div>
      {view.error !== null && (
        <InlineNotice tone="danger" live="alert">
          {view.error}
        </InlineNotice>
      )}
    </div>
  );
}
