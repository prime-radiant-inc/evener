// The shell's three lazily-fetched chunks - rail host, dock host, and the
// connect-provider dialog - can each fail on their own network request (see
// chunkRetry.ts). Each needs an error boundary scoped to its surface and a
// retry that swaps in a NEW lazy payload over a cache-busted URL; only the
// error chrome, the claim predicate, and any pre-retry cleanup differ.
import { Component, type ReactNode, useCallback, useState } from "react";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";

// Module scope, not per mount: a lazy() payload caches its resolved module, so
// one shared payload fetches the chunk once per page load and renders it
// straight away on every later mount. Publishing on retry is what makes one
// surface's recovery reach the others; a payload also caches a failure, which
// is why each module's resetXForTests publishes a fresh one.
export interface LazyChunkState<Chunk> {
  chunk: Chunk;
  version: number;
  publish(chunk: Chunk): void;
}

export type LazyChunkStore<Chunk> = StoreApi<LazyChunkState<Chunk>>;

export function createLazyChunkStore<Chunk>(create: () => Chunk): LazyChunkStore<Chunk> {
  return createStore<LazyChunkState<Chunk>>()((set) => ({
    chunk: create(),
    version: 0,
    publish: (chunk) => set((state) => ({ chunk, version: state.version + 1 })),
  }));
}

export interface ChunkRetry<Chunk> {
  // A retry publishes a new payload before it resolves, so an unmount/remount
  // during the retry shares the in-flight request instead of restoring the
  // rejected payload that caused the boundary.
  Chunk: Chunk;
  // Changes on every published payload, so a caller can key its boundary on
  // it and remount a surface still showing the old failure.
  version: number;
  retry: () => void;
  // True once a cache-busted retry has already failed: a deploy that removed
  // the hashed file 404s forever under a new query param, so the second
  // strike offers a page reload instead of another same-file fetch.
  reloadAvailable: boolean;
}

export function useChunkRetry<Chunk>(
  store: LazyChunkStore<Chunk>,
  create: (cacheBust: boolean) => Chunk,
  beforeRetry?: () => void,
): ChunkRetry<Chunk> {
  const chunk = useStore(store, (state) => state.chunk);
  const version = useStore(store, (state) => state.version);
  const [retryCount, setRetryCount] = useState(0);
  const retry = useCallback(() => {
    beforeRetry?.();
    setRetryCount((count) => count + 1);
    store.getState().publish(create(true));
  }, [beforeRetry, create, store]);
  return { Chunk: chunk, version, retry, reloadAvailable: retryCount > 0 };
}

export interface ChunkFailureFallbackProps {
  // The failed chunk's own message ("Failed to fetch dynamically imported
  // module: ..."), shown verbatim - a stated failure is worth more than a
  // generic apology.
  failure: string;
  retry: () => void;
  // True only once a cache-busted retry has already failed AND that failure
  // names a stale hashed asset: a reload is the only thing that can fetch a
  // newer URL, and counting retries alone would offer it for a logic bug.
  canReload: boolean;
}

export interface ChunkFailureBoundaryProps {
  // Swaps in a fresh lazy payload. The boundary clears its own failure state
  // alongside it - both halves are needed, and neither is any use without the
  // other.
  onRetry: () => void;
  reloadAvailable: boolean;
  // Called for every error the boundary claims (the dock cancels its pending
  // conversation handoff here). A declined error keeps unwinding instead.
  onCatch?: () => void;
  // Draws the surface's own failure chrome. A render callback, not a
  // component type, so a caller can close over its own props (the dialog's
  // onClose); components inside the returned element still mount normally.
  renderFailure: (failure: ChunkFailureFallbackProps) => ReactNode;
  children: ReactNode;
}

interface ChunkFailureBoundaryState {
  failure: string | null;
}

// getDerivedStateFromError sees only the error, never props, so the claim
// predicate is bound to the class. Declining keeps the error unwinding to the
// next boundary above rather than misreporting - and retrying - it as a failed
// network fetch.
export function createChunkFailureBoundary(
  claimError: (error: unknown) => boolean,
  isStaleFailure: (failure: string) => boolean = claimError,
) {
  return class ChunkFailureBoundary extends Component<ChunkFailureBoundaryProps, ChunkFailureBoundaryState> {
    state: ChunkFailureBoundaryState = { failure: null };

    componentDidCatch(): void {
      this.props.onCatch?.();
    }

    static getDerivedStateFromError(error: unknown): ChunkFailureBoundaryState {
      if (!claimError(error)) throw error;
      return { failure: error instanceof Error ? error.message : String(error) };
    }

    private retry = () => {
      this.setState({ failure: null });
      this.props.onRetry();
    };

    render(): ReactNode {
      if (this.state.failure === null) return this.props.children;
      return this.props.renderFailure({
        failure: this.state.failure,
        retry: this.retry,
        canReload: this.props.reloadAvailable && isStaleFailure(this.state.failure),
      });
    }
  };
}
