// The command palette overlay, mounted once by AppShell as a sibling of the
// toast region. T1 ships the overlay chrome only - an empty, dismissable
// dialog that opens/closes off the shared paletteStore. T3 fills the body
// (the three-mode search/command model + the 22-command registry) on this
// same store, replacing the empty content below.
import { Dialog } from "../../widgets";
import { closePalette, usePaletteStore } from "./paletteController";

export function CommandPalette() {
  const open = usePaletteStore((s) => s.open);
  return (
    <Dialog open={open} onClose={closePalette} title="Command palette">
      {null}
    </Dialog>
  );
}
