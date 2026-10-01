import { type ReactNode, useEffect, useRef } from "react";
import { chromeStore } from "../../shell/chromeStore";
import { cancelPaneFocus, consumePaneFocus } from "../../shell/workspace";
import { requireClass } from "../internal/requireClass";
import styles from "./panescaffold.module.css";

export interface PaneScaffoldProps {
  title: string;
  paneId?: string;
  focused?: boolean;
  scaffoldMarker?: string;
  mobileTitle?: string;
  cadence?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
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
};

/**
 * The standard pane chrome every pane type in the app uses: a header row
 * (truncating title + optional cadence slot + optional actions cluster), a
 * scrollable body, and an optional footer. Deliberately boring - this is
 * the most-copied layout primitive in the app, so every pane looks and
 * behaves the same way.
 */
export function PaneScaffold({
  title,
  paneId,
  focused = true,
  scaffoldMarker,
  mobileTitle,
  cadence,
  actions,
  footer,
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
  useEffect(() => {
    if (paneId === undefined) return;
    chromeStore.getState().setPaneTitleFor(paneId, title);
    return () => chromeStore.getState().setPaneTitleFor(paneId, null);
  }, [paneId, title]);

  const bodyRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (paneId === undefined) return;
    if (!focused) {
      cancelPaneFocus(paneId);
      return;
    }
    if (consumePaneFocus(paneId)) bodyRef.current?.focus();
  }, [focused, paneId]);

  return (
    <div className={CLASS.pane}>
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
    </div>
  );
}
