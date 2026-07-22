// The doc-pane opener (wave 8). openDocBeside is the ONE call every "open
// beside a file/image" producer uses (floor §3.7 read_file/edit_file/
// write_file cards, image cards): it builds a doc PaneRef from a session ref +
// cwd-relative path and routes it through openBeside. `kind` picks the file vs
// image data path inside the doc pane itself (T5 fills that pane).
//
// T1 ships the routing wiring against the locked signature; wave-8 T5 fills the
// doc pane (panes/doc/**) + self-registers the "doc" pane type. Until T6 fills
// openBeside, this no-ops cleanly (the pane isn't registered yet either), which
// is exactly the T1 smoke's "an empty doc-open call no-ops cleanly".
//
// The namespace import (not a named one) is deliberate: it lets openDoc.test.ts
// spy openBeside through the module object, the reliable vitest seam for
// asserting this delegation while the target is still a no-op.
import * as paneActions from "../../shell/paneActions";

export interface DocParams {
  session: string;
  path: string;
  kind: "file" | "image";
}

export function openDocBeside(params: DocParams): void {
  paneActions.openBeside({ type: "doc", params });
}
