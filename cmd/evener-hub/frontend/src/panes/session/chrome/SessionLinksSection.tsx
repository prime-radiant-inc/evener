import type { ThreadModel } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import type { JSX } from "react";
import { isWebHref, ListRow } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import { isFileHref } from "./NotesPanel";
import { PanelSection } from "./PanelSection";
import styles from "./sessionlinkssection.module.css";

const CLASS = {
  list: requireClass(styles.list, "sessionlinkssection.module.css", "list"),
  webLink: requireClass(styles.webLink, "sessionlinkssection.module.css", "webLink"),
};

/** Saved session links formatted on the same PanelSection + ListRow rhythm as
 * DetailsPanelBody. Absent when the session has no links. */
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
              {isWebHref(url.url) ? (
                // Always keep the destination visible beside an agent-authored label.
                <a
                  className={CLASS.webLink}
                  data-testid={`about-link-${url.id}`}
                  href={url.url}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <ListRow
                    variant="plain"
                    label={url.label || url.url}
                    meta={url.label && url.label !== url.url ? url.url : undefined}
                  />
                </a>
              ) : (
                <ListRow
                  variant="plain"
                  data-testid={`about-link-${url.id}`}
                  label={url.label || url.url}
                  meta={url.label ? url.url : undefined}
                  action={fileAction}
                />
              )}
            </li>
          );
        })}
      </ul>
    </PanelSection>
  );
}
