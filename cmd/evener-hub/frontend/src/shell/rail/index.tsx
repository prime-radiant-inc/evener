// Controller-owned mount point: AppShell imports { RailHost } from here and
// renders it as a sibling of DockHost (and inside StackHost's railSlot on
// mobile). The real host lives in ./RailHost - this barrel owns only the lazy
// seam in front of it.
//
// RailHost (Rail 1605 lines + RailRow + railNodes + navigation selectors)
// stays out of AppShell's initial chunk behind this seam: the rail is
// below-the-fold chrome beside the pane workspace, never the first-paint
// path. The Suspense fallback is null because AppShell's own content region
// already paints the workspace behind it; RailHost.test.tsx keeps importing
// ./RailHost directly, so its sync assertions never see this boundary.
//
// A rejected chunk lands on RailChunkBoundary below (the DockRegion
// chunk-boundary pattern), scoped to the rail: without it the lazy() rethrow
// would unmount the whole shell, which has no boundary of its own. Retry
// swaps in a fresh lazy component - a rejected payload rethrows forever, so
// re-rendering the old one could never recover.
//
// There is deliberately no `export { Rail }` here: a value re-export would
// keep an eager edge from this barrel to the 1605-line Rail module inside
// AppShell's initial chunk, defeating the split. Importers take ./Rail (or
// ./RailHost) directly.
import { Component, type JSX, type LazyExoticComponent, lazy, type ReactNode, Suspense, useState } from "react";
import { Button, EmptyState } from "../../widgets";

interface RailChunkBoundaryProps {
  // Swaps in a fresh lazy component to load the chunk again. The boundary
  // clears its own failure state alongside it - both halves are needed, and
  // neither is any use without the other.
  onRetry: () => void;
  children: ReactNode;
}

interface RailChunkBoundaryState {
  // The failed chunk's own message ("Failed to fetch dynamically imported
  // module: ..."), shown verbatim the way the rail shows a tree-load error -
  // a stated failure is worth more to whoever hits it than a generic apology.
  failure: string | null;
}

class RailChunkBoundary extends Component<RailChunkBoundaryProps, RailChunkBoundaryState> {
  state: RailChunkBoundaryState = { failure: null };

  static getDerivedStateFromError(error: unknown): RailChunkBoundaryState {
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
        title="Couldn't load the sidebar"
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

type RailHostComponent = () => JSX.Element;
type RailHostChunk = LazyExoticComponent<RailHostComponent>;

function lazyRailHost(): RailHostChunk {
  // RailHost is a named export, so the import() promise is adapted the same
  // way App.tsx's own DevHarnessRoute does for dev/DevHarness.tsx.
  return lazy(() => import("./RailHost").then((m) => ({ default: m.RailHost })));
}

// Module scope, not per mount: a lazy() component caches its resolved module
// on its own payload, so one shared component means the chunk is fetched once
// per page load and every later mount of this host (a breakpoint crossing, a
// drawer open/close) renders RailHost straight away instead of suspending
// again.
let railHost = lazyRailHost();

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  // A retry needs a NEW lazy component, not a re-render of the old one:
  // React.lazy stores the rejection on its payload and rethrows that same
  // error on every subsequent render, forever.
  const [Host, setHost] = useState<RailHostChunk>(railHost);
  const retry = () => {
    const nextHost = lazyRailHost();
    // Publish the new payload before it resolves so an unmount/remount during
    // the retry shares the in-flight request instead of restoring the rejected
    // payload that caused the boundary.
    railHost = nextHost;
    setHost(() => nextHost);
  };

  return (
    <RailChunkBoundary onRetry={retry}>
      <Suspense fallback={null}>
        <Host />
      </Suspense>
    </RailChunkBoundary>
  );
}
