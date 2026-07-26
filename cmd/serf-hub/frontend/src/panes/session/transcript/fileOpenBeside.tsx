// The "open beside" affordance for a file-referencing tool card (floor §3.7,
// presweep D1). A small quiet control that opens the file the card references in
// a read-only doc pane beside the session (the doc-pane open-beside producer
// path, PIN-A - see the routing note below). The referenced path is made
// relative to the session cwd (read
// from ThreadModel by ref - DECISION B); an out-of-cwd path earns NO affordance
// at all (the button renders nothing) - the same gate the legacy
// fileOpenBesideSpec/cwdRelative applied (renderer.js:2201-2251).
//
// Routing note: this opens the doc pane through paneActions.openBeside with a
// {type:"doc"} PaneRef - exactly what panes/doc/openDoc.ts's openDocBeside does
// (openDocBeside IS openBeside({type:"doc", params})). It deliberately does NOT
// import openDoc: openDoc.ts eagerly registers the doc pane (its own
// `import "./index"`), which - pulled in through ToolCallItem - would force that
// registration into the whole transcript tree at module load (and clobber the
// doc-pane test fixtures other panes' tests register). The doc pane is already
// registered at app boot by AppShell.tsx, and paneActions is already eagerly
// loaded by this tree (subagentModule), so this adds no new module-load side
// effect. The DocParams type is imported type-only (erased, no side effect).
// The namespace import of paneActions (not a named one) lets the test spy
// openBeside through the module object, the reliable vitest seam.
import type { MouseEvent } from "react";
import * as paneActions from "../../../shell/paneActions";
import { useThreadsStore } from "../../../stores/threads";
import { IconButton } from "../../../widgets";
import type { DocParams } from "../../doc/openDoc";

// The app's 16x16 stroke grammar (see PopoutHeaderAction.tsx's PopoutIcon,
// UserMessageItem.tsx's ForkGlyph): a document splitting into a second one
// beside it - two panels with an arrow crossing from the first into the
// second, currentColor so it inherits IconButton's variant colour exactly as
// the text label it replaces did (kata 3qnd - the surrounding pane chrome,
// Pop out/Fork from here, is all icons; this was the one text label left).
function OpenBesideIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <rect x="1.5" y="3" width="5" height="10" rx="1" stroke="currentColor" strokeWidth="1.3" fill="none" />
      <rect x="9.5" y="3" width="5" height="10" rx="1" stroke="currentColor" strokeWidth="1.3" fill="none" />
      <path
        d="M7 8H9.3M8 6.3L9.7 8L8 9.7"
        stroke="currentColor"
        strokeWidth="1.3"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

// cwdRelative expresses filePath relative to cwd, or undefined when it is not
// inside the cwd (so the affordance is withheld). Handles both shapes execenv's
// resolve() accepts (agent/execenv/local.go:1530-1533): an ABSOLUTE arg (strip
// the cwd prefix; out-of-cwd → undefined) and an already-RELATIVE arg (accepted
// as cwd-relative unless it escapes via a ".." segment).
export function cwdRelative(filePath: string, cwd: string): string | undefined {
  const p = filePath.trim();
  if (p === "" || cwd === "") return undefined;
  if (!p.startsWith("/")) {
    return p.split("/").includes("..") ? undefined : p;
  }
  const prefix = cwd.endsWith("/") ? cwd : `${cwd}/`;
  if (p === cwd) return undefined; // the cwd directory itself is not a file
  return p.startsWith(prefix) ? p.slice(prefix.length) : undefined;
}

// A file-path-bearing card whose path is an image file opens as an IMAGE
// (DECISION C): the doc pane then renders it through docImageURL (/doc/image,
// raw bytes) instead of the text/binary file path. The set mirrors what
// /doc/image actually serves (floor §1.5, output_images.go) - png/jpeg/gif/webp;
// SVG is deliberately excluded there (an XSS guard), so a .svg opens as a file
// (its source shown as text), not an image.
const IMAGE_EXT_RE = /\.(?:png|jpe?g|gif|webp)$/i;

// fileDocParams builds the DocParams for openDocBeside, or undefined when the
// ref/cwd is missing or the path is out of the cwd. `kind` is image for an
// image-extension path (rendered via docImageURL) and file otherwise.
export function fileDocParams(
  filePath: string | undefined,
  sessionRef: string | undefined,
  cwd: string | undefined,
): DocParams | undefined {
  if (filePath === undefined || sessionRef === undefined || cwd === undefined || cwd === "") return undefined;
  const rel = cwdRelative(filePath, cwd);
  if (rel === undefined) return undefined;
  return { session: sessionRef, path: rel, kind: IMAGE_EXT_RE.test(rel) ? "image" : "file" };
}

export function FileOpenBesideButton({ absPath, sessionRef }: { absPath: string; sessionRef: string }) {
  // cwd is snapshot-only ThreadModel state (DECISION B), stable for the pane's
  // life, so this selector returns a stable string - no re-renders from the
  // session's frequent streaming updates.
  const cwd = useThreadsStore((s) => s.threads.get(sessionRef)?.cwd);
  const params = fileDocParams(absPath, sessionRef, cwd);
  if (params === undefined) return null; // out-of-cwd / not hydrated yet → no affordance
  const docParams = params;
  function open(e: MouseEvent<HTMLButtonElement>) {
    // The button lives inside the row's <summary>; stop the click reaching the
    // summary's own toggle handler so opening a file never also flips the row.
    e.stopPropagation();
    paneActions.openBeside({ type: "doc", params: docParams });
  }
  // "Open beside" stays a literal, contiguous prefix (not "Open <path>
  // beside") so every existing /open beside/i query - ToolCallItem.test.tsx's
  // own affordance tests among them - still finds this control unchanged;
  // the path is appended for the same specificity the old dynamic title had.
  const name = `Open beside: ${docParams.path}`;
  return <IconButton label={name} title={name} icon={<OpenBesideIcon />} variant="quiet" size="sm" onClick={open} />;
}
