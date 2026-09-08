// Controller-owned mount point: AppShell imports { RailHost } from here and
// renders it as a sibling of DockHost (and inside StackHost's railSlot on
// mobile). RailHost wraps <Rail/> - which stays exported for RailHost and
// StackHost to mount directly. See the wave-6 report for the mount contract.
//
// RailHost (Rail 1605 lines + RailRow + railNodes + navigation selectors)
// stays out of AppShell's initial chunk behind this lazy seam: the rail is
// below-the-fold chrome beside the pane workspace, never the first-paint
// path. The fallback is null because AppShell's own content region already
// paints the workspace behind it; RailHost.test.tsx keeps importing
// ./RailHost directly, so its sync assertions never see this boundary.
import { lazy, Suspense, type JSX } from "react";
export { Rail } from "./Rail";

const LazyRailHost = lazy(() => import("./RailHost").then((m) => ({ default: m.RailHost })));

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  return (
    <Suspense fallback={null}>
      <LazyRailHost />
    </Suspense>
  );
}
