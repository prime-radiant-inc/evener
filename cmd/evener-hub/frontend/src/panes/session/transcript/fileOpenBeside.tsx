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
import { cwdRelative, isImagePath } from "@evener/appwire-client/docContent";
import * as paneActions from "../../../shell/paneActions";
import { useThreadsStore } from "../../../stores/threads";
import { useOptionalTranscriptRenderContext } from "../../../transcriptDisplay/renderContext";
import { OpenButton } from "../../../widgets";
import type { DocParams } from "../../doc/openDoc";

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

function FileOpenBesideButtonBody({ absPath, sessionRef, cwd }: { absPath: string; sessionRef: string; cwd?: string }) {
  const params = fileDocParams(absPath, sessionRef, cwd);
  if (params === undefined) return null; // out-of-cwd / not hydrated yet → no affordance
  const docParams = params;
  // "Open beside" stays a literal, contiguous prefix (not "Open <path>
  // beside") so every existing /open beside/i query - ToolCallItem.test.tsx's
  // own affordance tests among them - still finds this control unchanged;
  // the path is appended for the same specificity the old dynamic title had.
  const name = `Open beside: ${docParams.path}`;
  return <OpenButton label={name} onClick={() => paneActions.openBeside({ type: "doc", params: docParams })} />;
}

function LegacyFileOpenBesideButton({ absPath, sessionRef }: { absPath: string; sessionRef: string }) {
  const cwd = useThreadsStore((state) => state.threads.get(sessionRef)?.cwd);
  return <FileOpenBesideButtonBody absPath={absPath} sessionRef={sessionRef} cwd={cwd} />;
}

export function FileOpenBesideButton({
  absPath,
  sessionRef,
  cwd,
}: {
  absPath: string;
  sessionRef: string;
  cwd?: string;
}) {
  const context = useOptionalTranscriptRenderContext();
  return context === null ? (
    <LegacyFileOpenBesideButton absPath={absPath} sessionRef={sessionRef} />
  ) : (
    <FileOpenBesideButtonBody absPath={absPath} sessionRef={sessionRef} cwd={cwd ?? context.thread?.cwd} />
  );
}
