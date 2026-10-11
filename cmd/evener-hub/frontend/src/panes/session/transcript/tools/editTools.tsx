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
// classifier it uses for real diffs. Their summaries are
// @evener/appwire-client's toolSummaries, which the phone's step lines read
// too.

import {
  applyPatchSummary,
  editFileSummary,
  filePathArg,
  parseArgs,
  str,
  writeFileSummary,
} from "@evener/appwire-client";
import { DiffBlock } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { EditFileBody, WriteFileBody } from "./bodies";

registerToolRenderer({
  match: "edit_file",
  icon: "edit",
  fold: "consequential", // a mutation: it names the run it folds into
  summary: editFileSummary,
  body: EditFileBody,
  openBesidePath: filePathArg, // single-file mutation (floor §3.7)
});

registerToolRenderer({
  match: "write_file",
  icon: "edit",
  fold: "consequential",
  summary: writeFileSummary,
  body: WriteFileBody,
  openBesidePath: filePathArg, // single-file write (floor §3.7)
});

// apply_patch is deliberately NOT given openBesidePath: it can touch several
// files in one call, so there is no single file to open beside (floor §3.7
// excludes multi-target tools).

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
  summary: applyPatchSummary,
  body: ApplyPatchBody,
});
