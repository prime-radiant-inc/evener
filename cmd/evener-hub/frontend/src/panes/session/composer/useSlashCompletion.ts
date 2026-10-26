import { filterSlashMenuItems, type SlashMenuItem, type SlashToken, spliceSlashCommand } from "@evener/appwire-client";
import { type KeyboardEvent as ReactKeyboardEvent, type RefObject, useEffect, useState } from "react";
import type { TextEditor } from "./attachments/useAttachments";
import type { SkillEditorHandle } from "./SkillEditor";
import { optionId } from "./SlashCompletionMenu";

export interface UseSlashCompletionOptions {
  /** The merged command/skill catalog the menu filters (slashCompletion.ts's mergeSlashCommands). */
  catalog: SlashMenuItem[];
  /** False keeps the menu closed whatever the token is (a harness that loads no plugins). */
  enabled?: boolean;
  /** The listbox element id; option ids derive from it. */
  listboxId: string;
  initialToken?: () => SlashToken | null;
  editorRef: RefObject<SkillEditorHandle | null>;
  /** Mirror of the editor's current text, read when a built-in's invocation is spliced in. */
  textRef: RefObject<string>;
  textEditor: Pick<TextEditor, "write">;
  /**
   * Tab and Enter commit only when no modifier is held, so Shift+Tab keeps
   * moving focus and Mod+Enter keeps submitting while the menu is open.
   */
  plainKeysOnly?: boolean;
}

/**
 * The inline slash-command menu shared by the session composer and the spawn
 * form: the trailing-token match, the ArrowUp/Down highlight, the commit that
 * splices a completion into the draft, and the menu's keyboard handling.
 * Callers keep deciding when the token is recomputed (their own change
 * handlers call setToken) and render SlashCompletionMenu from the result.
 */
export function useSlashCompletion({
  catalog,
  enabled = true,
  listboxId,
  initialToken,
  editorRef,
  textRef,
  textEditor,
  plainKeysOnly = false,
}: UseSlashCompletionOptions) {
  const [token, setToken] = useState<SlashToken | null>(initialToken ?? null);
  const [highlighted, setHighlighted] = useState(0);
  // The menu is only ever open when a token matched AND the merged catalog has
  // at least one fuzzy label hit for it - a matched-but-empty token ("/zzz"
  // against a real catalog) shows no menu at all, same as no token matching.
  const items = token ? filterSlashMenuItems(catalog, token.query) : [];
  const open = enabled && token !== null && items.length > 0;
  const activeIndex = open ? Math.min(highlighted, items.length - 1) : -1;
  const activeId = activeIndex >= 0 ? optionId(listboxId, activeIndex) : null;

  // A freshly (re)matched token always starts highlighted at its first option:
  // an index carried over from the previous token's list is not a meaningful
  // position once the list itself has changed shape. The presence flip covers
  // Escape-dismiss then retype of the identical token (same start and query,
  // so those deps alone would keep a stale index).
  const tokenPresent = token !== null;
  // biome-ignore lint/correctness/useExhaustiveDependencies: token's start/query/presence are deliberate trigger-only deps - the effect body only resets the highlight, but must re-run whenever the token identity actually changes (a new match, the same match with a different query, or a dismiss→reopen flip)
  useEffect(() => {
    setHighlighted(0);
  }, [token?.start, token?.query, tokenPresent]);

  // The shared "the user chose this command" path for Tab/Enter and a mouse
  // click: splices the item's own invocation in at the token's start (never the
  // caret, which an earlier Escape-then-retype can leave mid-token), through
  // the same textEditor.write() seam every other programmatic edit uses, then
  // closes the menu and returns focus to the field. This only ever inserts the
  // invocation text; whether it executes as a built-in is the submit path's
  // own interception.
  function commit(item: SlashMenuItem): void {
    if (!token) return;
    // The editor replaces this range with one atomic mention and records the
    // text and activation metadata together in its undo history.
    if (item.kind !== "builtin" && item.canonicalName !== undefined) {
      // The editor refuses the insertion while an IME composition is live, so
      // only dismiss the menu for a skill that actually landed: closing it over
      // a token left as prose would tell the user something was staged that is
      // not in the request at all.
      const editor = editorRef.current;
      if (
        !(item.kind === "skill"
          ? editor?.insertSkill(token.start, token.end, item.canonicalName)
          : editor?.insertCommand(token.start, token.end, item.canonicalName))
      )
        return;
      setToken(null);
      editorRef.current?.focus();
      return;
    }
    const spliced = spliceSlashCommand(textRef.current, token, item.invocation);
    textEditor.write(spliced.text, spliced.caret);
    setToken(null);
    editorRef.current?.focus();
  }

  // ArrowUp/Down move the highlight (wrapping) over the caret rather than
  // moving it, Tab or Enter commits the highlighted option, Escape dismisses
  // without touching the draft. Returns true when the key was consumed, so a
  // committing Enter never reaches the caller's Enter-to-send routing.
  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): boolean {
    if (!open) return false;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setHighlighted((i) => (i + 1) % items.length);
      return true;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setHighlighted((i) => (i - 1 + items.length) % items.length);
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
    if (event.key === "Escape") {
      event.preventDefault();
      setToken(null);
      return true;
    }
    return false;
  }

  /** Drop the menu and its highlight, for a draft that was replaced outright. */
  function reset(): void {
    setToken(null);
    setHighlighted(0);
  }

  return { token, setToken, items, open, activeIndex, activeId, commit, handleKeyDown, reset };
}
