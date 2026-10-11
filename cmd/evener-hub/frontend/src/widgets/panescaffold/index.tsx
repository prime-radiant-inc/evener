import { type ReactNode, useEffect, useRef } from "react";
import { chromeStore } from "../../shell/chromeStore";
import { cancelPaneFocus, consumePaneFocus } from "../../shell/workspace";
import { requireClass } from "../internal/requireClass";
import styles from "./panescaffold.module.css";

export interface PaneScaffoldProps {
  title: string;
  paneId?: string;
  publishTabTitle?: boolean;
  focused?: boolean;
  focusSelector?: string;
  scaffoldMarker?: string;
  mobileTitle?: string;
  cadence?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  edgeFooter?: ReactNode;
  children: ReactNode;
}

const CLASS = {
  pane: requireClass(styles.pane, "panescaffold.module.css", "pane"),
  header: requireClass(styles.header, "panescaffold.module.css", "header"),
  title: requireClass(styles.title, "panescaffold.module.css", "title"),
  desktopTitle: requireClass(styles.desktopTitle, "panescaffold.module.css", "desktopTitle"),
  mobileTitle: requireClass(styles.mobileTitle, "panescaffold.module.css", "mobileTitle"),
  cadenceSlot: requireClass(styles.cadenceSlot, "panescaffold.module.css", "cadenceSlot"),
  actions: requireClass(styles.actions, "panescaffold.module.css", "actions"),
  body: requireClass(styles.body, "panescaffold.module.css", "body"),
  footer: requireClass(styles.footer, "panescaffold.module.css", "footer"),
  edgeFooter: requireClass(styles.edgeFooter, "panescaffold.module.css", "edgeFooter"),
};

/**
 * The standard pane chrome every pane type in the app uses: a header row
 * (truncating title + optional cadence slot + optional actions cluster), a
 * scrollable body, an optional padded footer, and an optional flush edge
 * footer. Deliberately boring - this is the most-copied layout primitive in
 * the app, so every pane looks and behaves the same way.
 */
export function PaneScaffold({
  title,
  paneId,
  publishTabTitle = true,
  focused = true,
  focusSelector,
  scaffoldMarker,
  mobileTitle,
  cadence,
  actions,
  footer,
  edgeFooter,
  children,
}: PaneScaffoldProps) {
  // The focused-title channel supplies StackHost's mobile top bar without
  // asking which host is showing this pane. mobileTitle wins where both are
  // given: the top bar is exactly the cramped slot mobileTitle exists for.
  // The cleanup clears on unmount so a closed pane never leaves a stale
  // title behind (breakpoint crossings unmount every pane - StackHost.tsx's
  // own comment - and the bar must not keep naming one).
  const publishedTitle = mobileTitle ?? title;
  useEffect(() => {
    chromeStore.getState().setPaneTitle(publishedTitle);
    return () => chromeStore.getState().setPaneTitle(null);
  }, [publishedTitle]);

  // A hydrated pane owns its display title. Desktop tabs consume that same
  // title without fetching another copy of the pane's underlying resource.
  // A different registered tab label keeps its own title owner.
  useEffect(() => {
    if (paneId === undefined || !publishTabTitle) return;
    chromeStore.getState().setPaneTitleFor(paneId, title);
    return () => chromeStore.getState().setPaneTitleFor(paneId, null);
  }, [paneId, title, publishTabTitle]);

  const paneRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (paneId === undefined) return;
    if (!focused) {
      cancelPaneFocus(paneId);
      return;
    }
    if (!consumePaneFocus(paneId)) return;
    const target = focusSelector ? paneRef.current?.querySelector<HTMLElement>(focusSelector) : null;
    if (target) target.focus({ preventScroll: true });
    else bodyRef.current?.focus();
  }, [focused, paneId, focusSelector]);

  return (
    <div ref={paneRef} className={CLASS.pane}>
      <div className={CLASS.header}>
        <h2 className={CLASS.title}>
          {mobileTitle === undefined ? (
            title
          ) : (
            <>
              <span className={CLASS.desktopTitle} data-testid="pane-title-desktop">
                {title}
              </span>
              <span className={CLASS.mobileTitle} data-testid="pane-title-mobile">
                {mobileTitle}
              </span>
            </>
          )}
        </h2>
        {cadence !== undefined && (
          <div className={CLASS.cadenceSlot} data-testid="pane-cadence-slot">
            {cadence}
          </div>
        )}
        {actions !== undefined && (
          <div className={CLASS.actions} data-testid="pane-actions">
            {actions}
          </div>
        )}
      </div>
      <div ref={bodyRef} className={CLASS.body} tabIndex={-1} data-pane-scaffold={scaffoldMarker}>
        {children}
      </div>
      {footer !== undefined && (
        <div className={CLASS.footer} data-testid="pane-footer">
          {footer}
        </div>
      )}
      {edgeFooter !== undefined && (
        <div className={CLASS.edgeFooter} data-testid="pane-edge-footer">
          {edgeFooter}
        </div>
      )}
    </div>
  );
}
