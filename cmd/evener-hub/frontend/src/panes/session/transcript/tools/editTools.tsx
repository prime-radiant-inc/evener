// Descriptors for the three file-mutating tools (parity checklist §2):
// edit_file, write_file, apply_patch. Ground truth (verified directly
// against agent/execenv/local.go and agent/session_tools_shell.go, not
// assumed from the legacy checklist): write_file's and edit_file's Output
// text are now plain confirmation strings ("wrote N bytes to X" / "edited X:
// N replacement(s)") - NOT diff text, unlike what the legacy diffRenderer
// assumed. edit_file's diff is still buildable by synthesizing it from the
// old_string/new_string INPUT args (legacy did this too, via editDiffText -
// that part of the checklist still holds). write_file has no prior-content
// signal anywhere on the wire, so no diff can be shown for it at all; this
// is a documented parity deviation, not an oversight. apply_patch's `patch`
// input arg is the model's own v4a patch text (agent/internal/tool/
// definitions.go's DefApplyPatch) - a distinct dialect from unified diff,
// but close enough (bare +/-/space-prefixed hunk lines) that DiffBlock's
// line classifier still colors it usefully, exactly as the legacy
// patchRenderer rendered from state.args.patch through the same
// classifier it uses for real diffs.

import type { ItemModel } from "../../../../protocol/model";
import { DiffBlock } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, str } from "./helpers";

// diffStats counts add/del lines the same way DiffBlock's own parser does
// (a "+++"/"---" file-header line never counts as content).
function diffStats(text: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of text.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) added += 1;
    else if (line.startsWith("-")) removed += 1;
  }
  return { added, removed };
}

function diffResultText(text: string): string {
  const { added, removed } = diffStats(text);
  return added === 0 && removed === 0 ? "ok" : `+${added} -${removed}`;
}

// editDiffText mirrors renderer-tools.js's editDiffText: a flat synthesized
// diff (no real @@ hunk range) - every old_string line prefixed "-", every
// new_string line prefixed "+", framed by "---"/"+++" file-name headers.
function editDiffText(path: string, oldString: string, newString: string): string {
  const oldLines = oldString.split("\n").map((l) => `-${l}`);
  const newLines = newString.split("\n").map((l) => `+${l}`);
  return [`--- ${path}`, `+++ ${path}`, ...oldLines, ...newLines].join("\n");
}

function EditFileBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const path = str(args, "file_path") ?? str(args, "path") ?? "";
  const oldString = str(args, "old_string") ?? "";
  const newString = str(args, "new_string") ?? "";
  return <DiffBlock unified={editDiffText(path, oldString, newString)} />;
}

// filePathArg reads the single-file arg the file tools share (file_path, or the
// legacy `path` alias) - the path the "open beside" affordance references.
function filePathArg(item: ItemModel): string | undefined {
  const args = parseArgs(item.argumentsJSON);
  return str(args, "file_path") ?? str(args, "path");
}

registerToolRenderer({
  match: "edit_file",
  icon: "edit",
  fold: "consequential", // a mutation: it names the run it folds into
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const path = str(args, "file_path") ?? str(args, "path") ?? "";
    const oldString = str(args, "old_string") ?? "";
    const newString = str(args, "new_string") ?? "";
    return `Edited ${path} · ${diffResultText(editDiffText(path, oldString, newString))}`;
  },
  body: EditFileBody,
  openBesidePath: filePathArg, // single-file mutation (floor §3.7)
});

function WriteFileBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <div>{output}</div>;
}

registerToolRenderer({
  match: "write_file",
  icon: "edit",
  fold: "consequential",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const path = str(args, "file_path") ?? str(args, "path") ?? "";
    return `Wrote ${path}`;
  },
  body: WriteFileBody,
  openBesidePath: filePathArg, // single-file write (floor §3.7)
});

// apply_patch is deliberately NOT given openBesidePath: it can touch several
// files in one call (patchTargets), so there is no single file to open beside
// (floor §3.7 excludes multi-target tools).

// patchTargets extracts unique file paths from v4a section headers
// ("*** Add/Update/Delete File: <path>"), preserving first-seen order -
// mirrors renderer-tools.js's patchTargets.
const PATCH_FILE_HEADER_RE = /^\*\*\* (?:Add|Update|Delete) File: (.+)$/;

function patchTargets(patch: string): string[] {
  const seen = new Set<string>();
  const targets: string[] = [];
  for (const line of patch.split("\n")) {
    const match = PATCH_FILE_HEADER_RE.exec(line);
    if (match?.[1] !== undefined && !seen.has(match[1])) {
      seen.add(match[1]);
      targets.push(match[1]);
    }
  }
  return targets;
}

function ApplyPatchBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const patch = str(args, "patch") ?? "";
  if (patch === "") return null;
  return <DiffBlock unified={patch} />;
}

registerToolRenderer({
  match: "apply_patch",
  icon: "edit",
  fold: "consequential",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const patch = str(args, "patch") ?? "";
    return `Patched ${patchTargets(patch).join(", ")} · ${diffResultText(patch)}`;
  },
  body: ApplyPatchBody,
});
