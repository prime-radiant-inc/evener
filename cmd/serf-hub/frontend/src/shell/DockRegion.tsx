// The dock half of the shell's workspace area: the lazily-fetched DockHost,
// plus the boundary that keeps its chunk's failure inside this region.
//
// AppShell mounts this where it used to mount <Suspense><DockHost/></Suspense>
// directly. Without a boundary, a chunk that 4xx/5xx's (a hub restarting
// mid-load, a deploy that replaced the hashed filename) makes React.lazy
// rethrow during render with no boundary anywhere above it - React unmounts
// the whole tree and the user gets a white page (kata 1s47). Scoped here, the
// same failure costs the workspace and nothing else: the rail, the connection
// banner, the toasts and the command palette all stay up.
import { Component, lazy, type ReactNode, Suspense, useState } from "react";
import { Button, EmptyState } from "../widgets";
import { loadDockHost } from "./dockHostChunk";

interface DockChunkBoundaryProps {
  // Swaps in a fresh lazy component to load the chunk again. The boundary
  // clears its own failure state alongside it - both halves are needed, and
  // neither is any use without the other.
  onRetry: () => void;
  children: ReactNode;
}

interface DockChunkBoundaryState {
  // The failed chunk's own message ("Failed to fetch dynamically imported
  // module: ..."), shown verbatim the way the rail shows a tree-load error -
  // a stated failure is worth more to whoever hits it than a generic apology.
  failure: string | null;
}

class DockChunkBoundary extends Component<DockChunkBoundaryProps, DockChunkBoundaryState> {
  state: DockChunkBoundaryState = { failure: null };

  static getDerivedStateFromError(error: unknown): DockChunkBoundaryState {
    return { failure: error instanceof Error ? error.message : String(error) };
  }

  private retry = () => {
    this.setState({ failure: null });
    this.props.onRetry();
  };

  render(): ReactNode {
    if (this.state.failure === null) return this.props.children;
    return (
      <EmptyState
        title="Couldn't load the workspace"
        hint={this.state.failure}
        action={
          <Button size="sm" onClick={this.retry}>
            Retry
          </Button>
        }
      />
    );
  }
}

function lazyDockHost() {
  // DockHost is a named export, so the import() promise is adapted the same
  // way App.tsx's own DevHarnessRoute does for dev/DevHarness.tsx.
  return lazy(() => loadDockHost().then((m) => ({ default: m.DockHost })));
}

// A lazy() component carries a call signature, so useState would otherwise
// read one as its own lazy-initializer callback and infer the state as
// whatever that call returns. Naming the state type keeps it a component.
type DockHostChunk = ReturnType<typeof lazyDockHost>;

// Module scope, not per mount: a lazy() component caches its resolved module
// on its own payload, so one shared component means the chunk is fetched once
// per page load and every later mount of this region (a breakpoint crossing,
// a route that swaps NotFound in and out) renders DockHost straight away
// instead of suspending again.
let dockHost = lazyDockHost();

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows (stores/tree.ts's own note); no production code
// should ever call it.
export function resetDockChunkForTests(): void {
  dockHost = lazyDockHost();
}

export function DockRegion() {
  // A retry needs a NEW lazy component, not a re-render of the old one:
  // React.lazy stores the rejection on its payload and rethrows that same
  // error on every subsequent render, forever.
  const [Host, setHost] = useState<DockHostChunk>(dockHost);

  return (
    <DockChunkBoundary onRetry={() => setHost(() => lazyDockHost())}>
      {/* fallback={null}: DockHost's own boot sequence (handleReady) already
          produces the very first meaningful paint (a routed pane, or welcome)
          synchronously once its chunk resolves - there is no useful
          intermediate state to show while just the dockview chunk itself is
          still loading, only this app's existing blank-shell moment stretched
          slightly longer. */}
      <Suspense fallback={null}>
        <Host />
      </Suspense>
    </DockChunkBoundary>
  );
}
