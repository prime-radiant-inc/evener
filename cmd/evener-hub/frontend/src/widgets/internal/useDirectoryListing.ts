// The directory-listing state machine shared by DirectoryPicker and PathField's
// completion panel. Both list a directory's children, carry recent directories,
// surface one error line, and drop a response a newer request superseded.
// They differ in policy: the picker validates before it lists, while the file
// field lists a completion prefix as typed. `browse` (immediate, validating when
// the source supplies a validator) and `reload` (debounced, raw prefix) are
// those two modes; the hook never picks one for the caller.

import { childrenPrefix, friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";

export interface DirectoryListingSource {
  /** Lists the children of `prefix`. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Recent directories. A rejection degrades to an empty list. */
  listRecents?: () => Promise<string[]>;
  /** Picker mode: validate a submitted directory before listing it. */
  validatePath?: (path: string, kind: string) => Promise<{ valid: boolean; path?: string; error?: string }>;
  /** Whether the listing includes files. Defaults to false. */
  includeFiles?: boolean;
  /** Fires when a request actually starts, with the exact prefix `complete`
   * receives. PathField records the filter the entries in hand were fetched
   * with here, so only a dotfile-boundary change forces a fresh listing. */
  onRequest?: (prefix: string) => void;
}

export interface DirectoryListingOptions {
  /** Default target for `browse()`/`reload()` when called without one. */
  path: string;
  /** Delay before `reload` lists. `browse` is always immediate. */
  debounceMs: number;
  source: DirectoryListingSource;
}

export interface DirectoryListing {
  /** The listing in hand, or null while a fetch is in flight or after it failed. */
  entries: string[] | null;
  recents: string[];
  /** The latest validation or listing error. Cleared when a request starts. */
  error: string | null;
  /** No entries and no error: a listing is due. */
  loading: boolean;
  /** Validate (when the source validates) and list `target` now. Resolves with
   * the canonical directory, or null when validation refused it or a newer
   * request superseded it. The listing itself continues after the resolve. */
  browse: (target?: string) => Promise<string | null>;
  /** List `prefix` after `debounceMs`, cancelling any pending request. */
  reload: (prefix?: string) => void;
  /** Cancel pending work and clear the listing and error. */
  cancel: () => void;
  /** Record (or clear with null) an error the hook itself cannot produce. */
  setError: (message: string | null) => void;
}

export function useDirectoryListing({ path, debounceMs, source }: DirectoryListingOptions): DirectoryListing {
  const [entries, setEntries] = useState<string[] | null>(null);
  const [recents, setRecents] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const request = useRef(0);
  const mounted = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // Actions read the latest injected operations. The mount-only recents load
  // uses the first render's source, as both callers did before.
  const latest = useRef(source);
  latest.current = source;

  function start(): number {
    clearTimeout(timer.current);
    const id = ++request.current;
    setEntries(null);
    setError(null);
    return id;
  }

  function list(prefix: string, id: number): void {
    const { complete, includeFiles, onRequest } = latest.current;
    onRequest?.(prefix);
    void complete(prefix, includeFiles ?? false).then(
      (children) => {
        if (mounted.current && request.current === id) setEntries(children);
      },
      (err) => {
        if (mounted.current && request.current === id) setError(friendlyErrorMessage(err));
      },
    );
  }

  async function browse(target: string = path): Promise<string | null> {
    const id = start();
    let canonical = target.trim();
    let prefix = target;
    if (latest.current.validatePath) {
      try {
        const result = await latest.current.validatePath(canonical, "dir");
        if (!mounted.current || request.current !== id) return null;
        if (!result.valid) {
          setError(result.error || "This path is not a directory.");
          return null;
        }
        canonical = result.path || canonical;
        prefix = childrenPrefix(canonical);
      } catch (err) {
        if (mounted.current && request.current === id) setError(friendlyErrorMessage(err));
        return null;
      }
    }
    list(prefix, id);
    return canonical;
  }

  function reload(prefix: string = path): void {
    const id = start();
    timer.current = setTimeout(() => list(prefix, id), debounceMs);
  }

  function cancel(): void {
    clearTimeout(timer.current);
    request.current += 1;
    setEntries(null);
    setError(null);
  }

  useEffect(() => {
    mounted.current = true;
    const loadRecents = latest.current.listRecents;
    loadRecents?.().then(
      (paths) => {
        if (mounted.current) setRecents(paths);
      },
      () => {
        if (mounted.current) setRecents([]);
      },
    );
    return () => {
      mounted.current = false;
      request.current += 1;
      clearTimeout(timer.current);
    };
  }, []);

  return { entries, recents, error, loading: entries === null && error === null, browse, reload, cancel, setError };
}
