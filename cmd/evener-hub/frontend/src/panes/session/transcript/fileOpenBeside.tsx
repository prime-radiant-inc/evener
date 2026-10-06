// The "open beside" affordance for a file-referencing tool card (floor §3.7,
// presweep D1). A small quiet control that opens the file the card references in
// a read-only doc pane beside the session (the doc-pane open-beside producer
// path, PIN-A - see the routing note below). The referenced path is made
// relative to the session cwd (read
// from ThreadModel by ref - DECISION B); an out-of-cwd path earns NO affordance
// at all (the button renders nothing) - the same gate the legacy
// fileOpenBesideSpec/cwdRelative applied (renderer.js:2201-2251).
//
// Routing note: this delegates to panes/doc/openDoc.ts so filename actions use
// the same source validation, retained binding, and real openBeside path as all
// other document producers.
import { bindFilePath, cwdRelative, isImagePath } from "@evener/appwire-client/docContent";
import { useOptionalTranscriptRenderContext } from "../../../transcriptDisplay/renderContext";
import { OpenButton } from "../../../widgets";
import { type DocParams, openDocBeside } from "../../doc/openDoc";

// fileDocParams builds the DocParams for openDocBeside, or undefined when the
// ref/cwd is missing or the path is out of the cwd. A path to an image
// /doc/image serves opens as an image (DECISION C), rendered through
// docImageURL instead of the text/binary file path; anything else, an .svg
// included, opens as a file.
export function fileDocParams(
  filePath: string | undefined,
  sessionRef: string | undefined,
  cwd: string | undefined,
): DocParams | undefined {
  if (filePath === undefined || sessionRef === undefined || cwd === undefined || cwd === "") return undefined;
  const rel = cwdRelative(filePath, cwd);
  if (rel === undefined) return undefined;
  return { session: sessionRef, path: rel, kind: isImagePath(rel) ? "image" : "file" };
}

function FileOpenBesideButtonBody({
  absPath,
  sessionRef,
  sourcePaneId,
  cwd,
}: {
  absPath: string;
  sessionRef: string;
  sourcePaneId: string;
  cwd?: string;
}) {
  const params = fileDocParams(absPath, sessionRef, cwd);
  const reference = cwd === undefined ? undefined : bindFilePath(absPath, cwd);
  if (params === undefined || reference === undefined) return null; // out-of-cwd / not hydrated yet → no affordance
  const docParams = params;
  // "Open beside" stays a literal, contiguous prefix (not "Open <path>
  // beside") so every existing /open beside/i query - ToolCallItem.test.tsx's
  // own affordance tests among them - still finds this control unchanged;
  // the path is appended for the same specificity the old dynamic title had.
  const name = `Open beside: ${docParams.path}`;
  return <OpenButton label={name} onClick={() => openDocBeside({ session: sessionRef, reference, sourcePaneId })} />;
}

export function FileOpenBesideButton({
  absPath,
  sessionRef,
  cwd,
  sourcePaneId,
}: {
  absPath: string;
  sessionRef: string;
  cwd?: string;
  sourcePaneId?: string;
}) {
  const context = useOptionalTranscriptRenderContext();
  const owner = sourcePaneId ?? context?.sourcePaneId;
  if (owner === undefined) return null;
  return (
    <FileOpenBesideButtonBody
      absPath={absPath}
      sessionRef={sessionRef}
      sourcePaneId={owner}
      cwd={cwd ?? context?.thread?.cwd}
    />
  );
}
