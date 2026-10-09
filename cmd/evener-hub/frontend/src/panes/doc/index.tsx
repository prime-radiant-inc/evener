// Registers the "doc" pane type. Split from DocPane.tsx (the component) for
// the same reason panes/welcome/index.tsx is split from Welcome.tsx: this
// module runs registerPane() eagerly and keeps DocPane's own chunk behind a
// lazy() import so it loads only when a doc pane actually opens.
//
// AppShell.tsx imports the panes that can appear in the initial layout to
// register them; a doc pane never appears there - it only ever opens on
// demand through openDocBeside - so openDoc.ts imports THIS module for its
// side effect instead, guaranteeing "doc" is registered before dockview is
// asked to build the panel (no AppShell chokepoint edit; panes self-register).
import { lazy } from "react";
import { registerPane } from "../../shell/paneRegistry";
import { filenameOf } from "./docFile";
import type { DocOpeningOrigin, DocParams } from "./openDoc";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value !== "";
}

// Validate one supplied origin exactly. A malformed origin returns null so the
// registry REFUSES the panel rather than silently downgrading it to the older
// origin-less shape; only a well-formed browser-origin or browser-readonly-session
// is accepted. No extra compatibility format is introduced here.
function parseOrigin(value: unknown): DocOpeningOrigin | null {
  if (!isRecord(value)) return null;
  if (value.kind === "browser-origin") {
    if (!nonEmptyString(value.paneId) || !nonEmptyString(value.compositionOwnerId)) return null;
    return { kind: "browser-origin", paneId: value.paneId, compositionOwnerId: value.compositionOwnerId };
  }
  if (value.kind === "browser-readonly-session") {
    if (!nonEmptyString(value.sourcePaneId)) return null;
    return { kind: "browser-readonly-session", sourcePaneId: value.sourcePaneId };
  }
  return null;
}

// parseParams accepts the current supported shape and the approved older shape
// without an origin, and rejects a supplied-but-malformed origin outright.
function parseDocParams(value: unknown): DocParams | null {
  if (!isRecord(value)) return null;
  if (!nonEmptyString(value.session) || !nonEmptyString(value.path)) return null;
  if (value.kind !== "file" && value.kind !== "image" && value.kind !== "text") return null;
  if (value.origin === undefined) return { session: value.session, path: value.path, kind: value.kind };
  const origin = parseOrigin(value.origin);
  if (origin === null) return null;
  return { session: value.session, path: value.path, kind: value.kind, origin };
}

registerPane<DocParams>({
  id: "doc",
  title: (params) => filenameOf(params.path),
  component: lazy(() => import("./DocPane")),
  parseParams: parseDocParams,
});
