import type { ThreadModel } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import type { JSX } from "react";
import { isWebHref } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import { isFileHref } from "./NotesPanel";
import { PanelSection } from "./PanelSection";
import styles from "./sessionlinkssection.module.css";

const CLASS = {
  list: requireClass(styles.list, "sessionlinkssection.module.css", "list"),
  webLink: requireClass(styles.webLink, "sessionlinkssection.module.css", "webLink"),
  row: requireClass(styles.row, "sessionlinkssection.module.css", "row"),
  text: requireClass(styles.text, "sessionlinkssection.module.css", "text"),
  label: requireClass(styles.label, "sessionlinkssection.module.css", "label"),
  url: requireClass(styles.url, "sessionlinkssection.module.css", "url"),
  action: requireClass(styles.action, "sessionlinkssection.module.css", "action"),
};

/** Saved session links formatted on the same PanelSection rhythm as
 * DetailsPanelBody, wrapping full destinations instead of truncating them.
 * Absent when the session has no links. */
export function SessionLinksSection({
  sessionRef,
  model,
  sourcePaneId,
}: {
  sessionRef: string;
  model: ThreadModel;
  sourcePaneId?: string;
}): JSX.Element | null {
  if (model.sessionUrls.length === 0) return null;
  return (
    <PanelSection title="Links">
      <ul className={CLASS.list} aria-label="Session links">
        {model.sessionUrls.map((url) => {
          const title = url.label || url.url;
          const showDestination = Boolean(url.label && url.label !== url.url);
          const body = (
            <span className={CLASS.text}>
              <span className={CLASS.label}>{title}</span>
              {showDestination ? <span className={CLASS.url}>{url.url}</span> : null}
            </span>
          );
          if (isWebHref(url.url)) {
            return (
              <li key={url.id}>
                {/* Always keep the full destination visible beside an agent-authored label. */}
                <a
                  className={CLASS.webLink}
                  data-testid={`about-link-${url.id}`}
                  href={url.url}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {body}
                </a>
              </li>
            );
          }
          const fileAction =
            sourcePaneId !== undefined && isFileHref(url.url) ? (
              <FileOpenBesideButton
                absPath={fileURLToPath(url.url)}
                sessionRef={sessionRef}
                cwd={model.cwd}
                sourcePaneId={sourcePaneId}
              />
            ) : undefined;
          return (
            <li key={url.id}>
              <div className={CLASS.row} data-testid={`about-link-${url.id}`}>
                {body}
                {fileAction !== undefined ? <span className={CLASS.action}>{fileAction}</span> : null}
              </div>
            </li>
          );
        })}
      </ul>
    </PanelSection>
  );
}
