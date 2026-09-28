// Diff text for the file-editing tools, shared by the web and the phone.
// edit_file's diff is synthesized from its old_string/new_string arguments:
// a flat diff with no real @@ hunk ranges. apply_patch carries its own patch
// text. write_file has no prior content anywhere on the wire, so it has no diff.

// diffStats counts add/del lines the same way the web's DiffBlock parser does
// (a "+++"/"---" file-header line never counts as content).
export function diffStats(text: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of text.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) added += 1;
    else if (line.startsWith("-")) removed += 1;
  }
  return { added, removed };
}

// editDiffText mirrors renderer-tools.js's editDiffText: a flat synthesized
// diff (no real @@ hunk range) - every old_string line prefixed "-", every
// new_string line prefixed "+", framed by "---"/"+++" file-name headers.
export function editDiffText(path: string, oldString: string, newString: string): string {
  const oldLines = oldString.split("\n").map((l) => `-${l}`);
  const newLines = newString.split("\n").map((l) => `+${l}`);
  return [`--- ${path}`, `+++ ${path}`, ...oldLines, ...newLines].join("\n");
}
