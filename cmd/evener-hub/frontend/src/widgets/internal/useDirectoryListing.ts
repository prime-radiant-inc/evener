// The directory listing both the directory picker and the path field's browse
// panel need: the children of one prefix, loaded through a caller-supplied
// `complete`, with the stale-response, unmount and error-degradation rules
// that keep a typed or navigated path from displaying another directory's
// rows. The two panels differ only in how they degrade a failed listing and
// whether requests are debounced, which are options here.

import { friendlyErrorMessage } from "@evener/appwire-client";
import { useCallback, useEffect, useRef, useState } from "react";

export interface DirectoryListingOptions {
  /** Lists one prefix's children. The picker passes directories only; the
   * path field browses files too. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Recent directories for the panel's Recent group; omitted when the
   * surface has none. */
  listRecents?: () => Promise<string[]>;
  includeFiles?: boolean;
  /** Coalesces requests by this many milliseconds. Omitted or 0 starts each
   * request immediately. */
  debounceMs?: number;
  /** What a rejected listing leaves behind. "empty" (the default) degrades to
   * an empty list plus the error, so the panel can distinguish "nothing here"
   * from "could not read this"; "null" drops the list and leaves the error,
   * for a panel that renders the error in place of the list. */
  entriesOnError?: "empty" | "null";
  /** Called when a request actually starts, with the prefix it fetches: the
   * path field records which filter the entries in hand were fetched with. */
  onRequest?: (prefix: string) => void;
}

export interface DirectoryListing {
  /** The newest listing, or null while one is in flight (or after a dropped
   * one). */
  entries: string[] | null;
  /** The newest listing's failure, cleared by the next request. */
  error: string | null;
  recents: string[];
  /** Fetch a prefix now, replacing any pending request. */
  browse: (prefix: string) => void;
  /** Replace the list with a pending request after the debounce window. */
  schedule: (prefix: string) => void;
  /** Drop the list and ignore any request in flight: the caller is about to
   * name a different prefix. */
  invalidate: () => void;
}

export function useDirectoryListing({
  complete,
  listRecents,
  includeFiles = false,
  debounceMs = 0,
  entriesOnError = "empty",
  onRequest,
}: DirectoryListingOptions): DirectoryListing {
  const [entries, setEntries] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [recents, setRecents] = useState<string[]>([]);
  const mounted = useRef(true);
  // Monotonic request id: a response older than the newest request is dropped,
  // so an out-of-order resolution never overwrites fresher entries.
  const requestId = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // The callbacks belong to the current render; the request started by an
  // earlier render still calls the latest ones, as the panels' inline props
  // expect.
  const latest = useRef({ complete, includeFiles, entriesOnError, onRequest });
  latest.current = { complete, includeFiles, entriesOnError, onRequest };

  const clearTimer = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = undefined;
  }, []);

  const run = useCallback((prefix: string) => {
    const options = latest.current;
    options.onRequest?.(prefix);
    const id = ++requestId.current;
    setEntries(null);
    setError(null);
    options.complete(prefix, options.includeFiles).then(
      (result) => {
        if (mounted.current && requestId.current === id) setEntries(result);
      },
      (err) => {
        if (!mounted.current || requestId.current !== id) return;
        // A rejection must never read as "Nothing here." - that would hide a
        // real failure (a closed client, a permissions error) behind the same
        // words an actually-empty directory gets.
        if (options.entriesOnError === "null") {
          setError(friendlyErrorMessage(err));
        } else {
          setEntries([]);
          setError(friendlyErrorMessage(err));
        }
      },
    );
  }, []);

  const browse = useCallback(
    (prefix: string) => {
      clearTimer();
      run(prefix);
    },
    [clearTimer, run],
  );

  // The list is dropped the INSTANT the caller names a new directory, not when
  // the debounced request fires: the header already describes the new
  // directory, and rendering the previous one's rows under it for a whole
  // debounce window makes every row a lie. Any response still in flight is
  // discarded for the same reason.
  const schedule = useCallback(
    (prefix: string) => {
      clearTimer();
      setEntries(null);
      setError(null);
      requestId.current += 1;
      timer.current = setTimeout(() => run(prefix), debounceMs);
    },
    [clearTimer, debounceMs, run],
  );

  const invalidate = useCallback(() => {
    clearTimer();
    requestId.current += 1;
    setEntries(null);
    setError(null);
  }, [clearTimer]);

  // The recents loader is captured for the panel's lifetime: a panel opens
  // once, and the caller's inline loader identity must not refetch it.
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only; the injected loader belongs to this panel's lifetime
  useEffect(() => {
    mounted.current = true;
    if (listRecents !== undefined) {
      listRecents().then(
        (paths) => {
          if (mounted.current) setRecents([...new Set(paths)]);
        },
        () => {
          if (mounted.current) setRecents([]);
        },
      );
    }
    return () => {
      mounted.current = false;
      requestId.current += 1;
      clearTimeout(timer.current);
    };
  }, []);

  return {
    entries,
    error,
    recents,
    browse,
    schedule,
    invalidate,
  };
}
