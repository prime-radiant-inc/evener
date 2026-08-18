// Mount contract: render <ToastRegion /> exactly once, near the app root -
// e.g. inside AppShell.tsx's outermost div, alongside <ConnectionBanner/> -
// never per-pane, never per-route. It owns nothing but widgets/toast's own
// <Toast/> region (the aria-live="polite" queue - see widgets/toast/
// index.tsx); any code anywhere in the tree can enqueue a toast via
// useToasts() (re-exported from "../../widgets") without importing this
// component at all - the two sides only need to agree on Toast's own
// module-singleton queue (widgets/toast/store.ts), not a shared prop or
// context, so mounting <ToastRegion/> is the ONLY step needed to make every
// useToasts().push() call anywhere in the app actually render.
//
// AppShell.tsx does not mount this yet - per this task's own constraint
// ("controller wires the toast region/chrome at merge"), wiring it in is
// the wave's merge/integration step, one line alongside the existing
// ConnectionBanner render:
//   <ConnectionBanner state={connectionState} />
//   <ToastRegion />
import { Toast } from "../../widgets";

export function ToastRegion() {
  return <Toast />;
}
