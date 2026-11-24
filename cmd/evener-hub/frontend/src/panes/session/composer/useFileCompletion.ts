import {
  type AppwireClientLike,
  createFileMentionStore,
  type FileMentionTarget,
  type FileMentionToken,
  type FileSearchItem,
  normalizeHost,
  parseFileMentionToken,
  parseSlashToken,
  type SlashToken,
  spliceFileMention,
} from "@evener/appwire-client";
import {
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { fileOptionId } from "./FileCompletionMenu";
import type { SkillEditorHandle } from "./SkillEditor";

const FILE_LOADER_DELAY_MS = 150;

export function resolveCompletionTokens(
  maskedText: string,
  caret: number,
  isPaste = false,
  protectedRanges?: readonly { start: number; end: number }[],
): { slashToken: SlashToken | null; fileToken: FileMentionToken | null } {
  const slashToken = parseSlashToken(maskedText, caret);
  return {
    slashToken,
    fileToken: isPaste || slashToken !== null ? null : parseFileMentionToken(maskedText, caret, protectedRanges),
  };
}

export interface UseFileCompletionOptions {
  client: Pick<AppwireClientLike, "request"> | null;
  target: FileMentionTarget | null;
  enabled?: boolean;
  editorRef: RefObject<SkillEditorHandle | null>;
  textRef: RefObject<string>;
  draft: string;
  plainKeysOnly?: boolean;
  getProtectedRanges?: () => readonly { start: number; end: number }[];
}

export function useFileCompletion({
  client,
  target,
  enabled = true,
  editorRef,
  textRef,
  draft,
  plainKeysOnly = false,
  getProtectedRanges,
}: UseFileCompletionOptions) {
  const listboxId = useId();
  const [store] = useState(() => createFileMentionStore());
  const snapshot = useSyncExternalStore(store.subscribe, store.getState, store.getInitialState);
  const [tokenState, setTokenState] = useState<{ token: FileMentionToken | null; immediate: boolean }>({
    token: null,
    immediate: false,
  });
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [showLoader, setShowLoader] = useState(false);

  // Prevents a synchronous onChange dispatched from SkillEditor.replaceTextRange
  // inside commit() from re-opening the menu (e.g. when committing before ",").
  const committingRef = useRef(false);
  const tokenRef = useRef<FileMentionToken | null>(null);
  tokenRef.current = tokenState.token;
  const getProtectedRangesRef = useRef(getProtectedRanges);
  getProtectedRangesRef.current = getProtectedRanges;
  const lastObservedDraftRef = useRef(draft);

  const targetRef = useRef<FileMentionTarget | null>(target);
  targetRef.current = target;
  const targetKind = target?.kind ?? "";
  const targetScope =
    target === null ? "" : target.kind === "session" ? target.ref : normalizeHost(target.host ?? null);
  const targetCwd = target?.kind === "cwd" ? target.cwd.trim() : "";

  const dismiss = useCallback(() => {
    setTokenState({ token: null, immediate: false });
    store.clear();
  }, [store]);

  const setToken = useCallback(
    (next: FileMentionToken | null, opts?: { immediate?: boolean }) => {
      if (committingRef.current) return;
      lastObservedDraftRef.current = textRef.current;
      setTokenState((prev) => {
        if (next === null) {
          if (prev.token === null && !prev.immediate) return prev;
          return { token: null, immediate: false };
        }
        const initialBareAt = prev.token === null && next.query === "";
        const immediate = Boolean(opts?.immediate) || initialBareAt;
        if (
          prev.token !== null &&
          prev.token.start === next.start &&
          prev.token.end === next.end &&
          prev.token.query === next.query &&
          prev.token.quoted === next.quoted &&
          prev.immediate === immediate
        ) {
          return prev;
        }
        return { token: next, immediate };
      });
    },
    [textRef],
  );

  const token = tokenState.token;
  const tokenPresent = token !== null;

  // Controlled draft mutations (image paste, marker removal, quote insert, resets)
  // change `draft` without firing editor onChange; drop stale token offsets immediately.
  useLayoutEffect(() => {
    if (draft === lastObservedDraftRef.current) return;
    lastObservedDraftRef.current = draft;
    dismiss();
  }, [draft, dismiss]);

  // Reset highlight index to 0 whenever the active token, query, presence, or target changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: trigger-only deps reset highlight index to 0 when token or scope shifts
  useEffect(() => {
    setHighlightedIndex(0);
  }, [token?.start, token?.query, tokenPresent, targetKind, targetScope, targetCwd]);

  // Issue debounced (or immediate) file search against the current target.
  // biome-ignore lint/correctness/useExhaustiveDependencies: primitive target fields prevent redundant timer cancels on caller object re-creation
  useEffect(() => {
    const activeTarget = targetRef.current;
    if (!enabled || client === null || activeTarget === null || token === null) {
      store.clear();
      return;
    }
    return store.search({
      client,
      target: activeTarget,
      query: token.query,
      enabled: true,
      immediate: tokenState.immediate,
    });
  }, [store, enabled, client, targetKind, targetScope, targetCwd, token?.query, tokenPresent, tokenState.immediate]);

  // Only show Loader after 150ms when loading with zero optimistic rows.
  const waitingForRows =
    enabled && client !== null && target !== null && tokenPresent && snapshot.loading && snapshot.items.length === 0;
  useEffect(() => {
    if (!waitingForRows) {
      setShowLoader(false);
      return;
    }
    const timer = setTimeout(() => setShowLoader(true), FILE_LOADER_DELAY_MS);
    return () => clearTimeout(timer);
  }, [waitingForRows]);

  const items = enabled && tokenPresent ? snapshot.items : [];
  const open = enabled && tokenPresent && (items.length > 0 || showLoader);
  const activeIndex = items.length > 0 ? Math.max(0, Math.min(highlightedIndex, items.length - 1)) : -1;
  const activeId = open && activeIndex >= 0 ? fileOptionId(listboxId, activeIndex) : null;

  const commit = useCallback(
    (item: FileSearchItem) => {
      const currentToken = tokenRef.current;
      const editor = editorRef.current;
      if (!currentToken || !editor) return;
      const currentText = textRef.current;
      if (currentText !== lastObservedDraftRef.current) {
        lastObservedDraftRef.current = currentText;
        dismiss();
        return;
      }
      const replaceEnd = currentToken.end;
      const spliced = spliceFileMention(currentText, currentToken, item);
      const insertedLength = spliced.text.length - (currentText.length - (replaceEnd - currentToken.start));
      const inserted = spliced.text.slice(currentToken.start, currentToken.start + insertedLength);
      const caretOffset = spliced.caret - currentToken.start;

      committingRef.current = true;
      let replaced = false;
      try {
        replaced = editor.replaceTextRange(currentToken.start, replaceEnd, inserted, caretOffset);
      } finally {
        committingRef.current = false;
      }
      if (!replaced) return;
      lastObservedDraftRef.current = textRef.current;

      if (spliced.keepOpen) {
        const nextToken = parseFileMentionToken(spliced.text, spliced.caret, getProtectedRangesRef.current?.());
        setTokenState({ token: nextToken, immediate: true });
      } else {
        setTokenState({ token: null, immediate: false });
      }
      editor.focus();
    },
    [dismiss, editorRef, textRef],
  );

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>): boolean => {
      if (!open) return false;
      if (event.key === "Escape") {
        event.preventDefault();
        setTokenState({ token: null, immediate: false });
        return true;
      }
      if (items.length === 0) return false;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const delta = event.key === "ArrowDown" ? 1 : -1;
        setHighlightedIndex((prev) => {
          const base = Math.max(0, Math.min(prev, items.length - 1));
          return (base + delta + items.length) % items.length;
        });
        return true;
      }
      const plain = !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
      const modifiersAllowed = !plainKeysOnly || plain;
      if (
        (event.key === "Tab" && modifiersAllowed) ||
        (event.key === "Enter" && modifiersAllowed && !event.nativeEvent.isComposing)
      ) {
        event.preventDefault();
        event.stopPropagation();
        const chosen = items[activeIndex] ?? items[0];
        if (chosen) commit(chosen);
        return true;
      }
      return false;
    },
    [open, items, plainKeysOnly, activeIndex, commit],
  );

  const reset = useCallback(() => {
    lastObservedDraftRef.current = textRef.current;
    dismiss();
  }, [dismiss, textRef]);

  return {
    token,
    setToken,
    items,
    open,
    showLoader,
    truncated: snapshot.truncated,
    activeIndex,
    activeId,
    listboxId,
    commit,
    handleKeyDown,
    reset,
  };
}
