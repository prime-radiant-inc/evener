// Controller-owned mount point: AppShell imports { RailHost } from here and
// renders it as a sibling of DockHost (and inside StackHost's railSlot on
// mobile). RailHost wraps <Rail/> - which stays exported for RailHost and
// StackHost to mount directly. See the wave-6 report for the mount contract.
export { Rail } from "./Rail";
export { RailHost } from "./RailHost";
