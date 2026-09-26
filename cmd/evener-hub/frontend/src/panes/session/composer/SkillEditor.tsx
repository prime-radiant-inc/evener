import { baseKeymap, deleteSelection } from "prosemirror-commands";
import { closeHistory, history, redo, undo } from "prosemirror-history";
import { keymap } from "prosemirror-keymap";
import { Fragment, type Node as ProseMirrorNode } from "prosemirror-model";
import { type Command, EditorState, Plugin, TextSelection } from "prosemirror-state";
import { EditorView } from "prosemirror-view";
import { forwardRef, type HTMLAttributes, useImperativeHandle, useLayoutEffect, useRef } from "react";
import { flushSync } from "react-dom";
import {
  completeSkillReferenceAt,
  documentPositionToTextOffset,
  isSkillTokenCharacter,
  parseSkillDocument,
  type SkillEditorValue,
  serializeSkillDocument,
  skillSchema,
  textOffsetToDocumentPosition,
} from "./skillDocument";
import styles from "./skilleditor.module.css";

export type { SkillEditorValue } from "./skillDocument";

export interface SkillEditorHandle {
  focus(): void;
  /** UTF-16 offset in serialized text. */
  getCursor(): number;
  getSelection(): { start: number; end: number };
  setSelection(start: number, end?: number): void;
  /** False when the editor could not take the insertion (an active IME
   * composition), so the caller does not dismiss its own affordance for a skill
   * that never landed. */
  insertSkill(start: number, end: number, name: string): boolean;
}

export interface SkillEditorProps {
  value: SkillEditorValue;
  onChange(value: SkillEditorValue, cursor: number): void;
  onKeyDown?: HTMLAttributes<HTMLDivElement>["onKeyDown"];
  onPaste?: HTMLAttributes<HTMLDivElement>["onPaste"];
  onFocus?: HTMLAttributes<HTMLDivElement>["onFocus"];
  onBlur?: HTMLAttributes<HTMLDivElement>["onBlur"];
  placeholder?: string;
  minLines?: number;
  /**
   * Bumped by the caller whenever the incoming value is authoritative - a draft
   * or recovery arriving from storage. Such a value rebuilds the document, so
   * every complete reference it selects becomes a chip again. A programmatic
   * edit is not authoritative: it patches, and what it inserts stays prose.
   */
  restoreEpoch?: number;
  "aria-label"?: string;
  "aria-controls"?: string;
  "aria-activedescendant"?: string;
  skillDetails?(name: string): string;
}

function deleteAtom(direction: -1 | 1): Command {
  return (state, dispatch) => {
    if (!state.selection.empty) return deleteSelection(state, dispatch);
    const { $from } = state.selection;
    const adjacent = direction === -1 ? $from.nodeBefore : $from.nodeAfter;
    if (adjacent?.type !== skillSchema.nodes.skill) return false;
    const from = direction === -1 ? $from.pos - adjacent.nodeSize : $from.pos;
    dispatch?.(closeHistory(state.tr.delete(from, from + adjacent.nodeSize)));
    return true;
  };
}

/** Each skill atom's UTF-16 offset in the serialized text, with its doc position. */
function skillAtomOffsets(doc: ProseMirrorNode): { offset: number; pos: number; name: string }[] {
  const atoms: { offset: number; pos: number; name: string }[] = [];
  let offset = 0;
  doc.forEach((node, pos) => {
    if (node.isText) offset += node.nodeSize;
    else {
      atoms.push({ offset, pos, name: node.attrs.name });
      offset += node.attrs.name.length + 1;
    }
  });
  return atoms;
}

// A chip is a whole reference or it is nothing: typing a token character
// straight against its label (`/skill-1d`, `BEFORE_/skill-1`) makes text the
// parser no longer reads as that skill, so the atom would survive on screen
// and then be dropped, without warning, by the next re-derivation - another
// write, a remount, a recovery. Separate the two instead, in the same edit, so
// the label stays whole and nothing the user typed is lost. Only the side the
// typed run actually collided with gains a space; characters that already
// bound the reference (`/skill-1,`) are left exactly as typed.
const skillIntegrity = new Plugin({
  appendTransaction: (transactions, _oldState, newState) => {
    if (!transactions.some((transaction) => transaction.docChanged)) return null;
    const text = serializeSkillDocument(newState.doc).text;
    const broken = skillAtomOffsets(newState.doc).filter(
      (atom) => completeSkillReferenceAt(text, atom.offset, [atom.name]) !== atom.name,
    );
    if (broken.length === 0) return null;
    // Deliberately NOT closeHistory: the separator exists only because of the
    // edit that broke the reference, so it belongs to that edit's history
    // event. As its own event, one undo would revert only the space - leaving
    // `/skill-1d`, which this plugin would immediately re-separate, making undo
    // look like a no-op and the typed character unreachable.
    const tr = newState.tr;
    // Back to front, and each atom's own trailing side before its leading one,
    // so an insertion never invalidates a position still to be used. Two atoms
    // that end up adjacent share one boundary, and one space is all it needs:
    // a second insert at the same position would leave the user with whitespace
    // they never typed.
    const spaced = new Set<number>();
    for (const atom of broken.reverse()) {
      const prefixBlocked = atom.offset > 0 && isSkillTokenCharacter(text.charAt(atom.offset - 1));
      const separated = prefixBlocked ? `${text.slice(0, atom.offset)} ${text.slice(atom.offset)}` : text;
      const suffixBlocked =
        completeSkillReferenceAt(separated, atom.offset + (prefixBlocked ? 1 : 0), [atom.name]) !== atom.name;
      if (suffixBlocked && !spaced.has(atom.pos + 1)) {
        tr.insertText(" ", atom.pos + 1);
        spaced.add(atom.pos + 1);
      }
      if (prefixBlocked && !spaced.has(atom.pos)) {
        tr.insertText(" ", atom.pos);
        spaced.add(atom.pos);
      }
    }
    return tr;
  },
});

/** Whether a serialized offset falls strictly inside an atom's own label. */
function atomInteriorAt(doc: ProseMirrorNode, offset: number): boolean {
  return skillAtomOffsets(doc).some((atom) => offset > atom.offset && offset < atom.offset + atom.name.length + 1);
}

/** Marks a transaction that applies the controlled value rather than an edit. */
const externalSync = "skillEditorExternalSync";

/** Length of the leading run two serialized values share. */
function sharedPrefixLength(before: string, after: string): number {
  const limit = Math.min(before.length, after.length);
  let index = 0;
  while (index < limit && before[index] === after[index]) index++;
  return index;
}

/** Length of the trailing run two serialized values share, past `prefix`. */
function sharedSuffixLength(before: string, after: string, prefix: number): number {
  const limit = Math.min(before.length, after.length) - prefix;
  let count = 0;
  while (count < limit && before[before.length - 1 - count] === after[after.length - 1 - count]) count++;
  return count;
}

function createState(value: SkillEditorValue): EditorState {
  const newline: Command = (state, dispatch) => {
    dispatch?.(state.tr.insertText("\n"));
    return true;
  };
  return EditorState.create({
    doc: parseSkillDocument(value),
    plugins: [
      history(),
      skillIntegrity,
      keymap({
        "Mod-z": undo,
        "Mod-Shift-z": redo,
        "Mod-y": redo,
        Enter: newline,
        "Shift-Enter": newline,
        Backspace: deleteAtom(-1),
        Delete: deleteAtom(1),
      }),
      keymap(baseKeymap),
    ],
  });
}

function updateDetails(dom: HTMLElement, name: string, props: SkillEditorProps) {
  const details = props.skillDetails?.(name) ?? name;
  dom.title = details;
  dom.setAttribute("aria-label", `/${name}: ${details}`);
}

/** A controlled value boundary around a persistent real ProseMirror editor. */
export const SkillEditor = forwardRef<SkillEditorHandle, SkillEditorProps>(function SkillEditor(props, ref) {
  const host = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const latest = useRef(props);
  const appliedRestoreRef = useRef(props.restoreEpoch ?? 0);
  useLayoutEffect(() => {
    latest.current = props;
  });

  useImperativeHandle(
    ref,
    () => ({
      focus: () => viewRef.current?.focus(),
      getCursor: () => {
        const view = viewRef.current;
        // `selection.from` is the lower offset, which is what the textarea's
        // `selectionStart` reported. `head` is the focus end, so a forward
        // non-collapsed selection would anchor attachment-marker stripping at
        // the wrong end of the range.
        return view ? documentPositionToTextOffset(view.state.doc, view.state.selection.from) : 0;
      },
      getSelection: () => {
        const view = viewRef.current;
        return view
          ? {
              start: documentPositionToTextOffset(view.state.doc, view.state.selection.from),
              end: documentPositionToTextOffset(view.state.doc, view.state.selection.to),
            }
          : { start: 0, end: 0 };
      },
      setSelection: (start, end = start) => {
        const view = viewRef.current;
        if (!view) return;
        const from = textOffsetToDocumentPosition(view.state.doc, start);
        const to = start === end ? from : textOffsetToDocumentPosition(view.state.doc, end, 1);
        view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, from, to)));
      },
      insertSkill: (start, end, name) => {
        const view = viewRef.current;
        if (!view || view.composing) return false;
        const { doc } = view.state;
        const from = textOffsetToDocumentPosition(doc, start);
        const to = textOffsetToDocumentPosition(doc, end, 1);
        const suffix = doc.textBetween(to, doc.content.size);
        // Ask the parser's own rule whether the label already reads as a whole
        // reference with nothing added - punctuation that bounds it needs no
        // separator (`/review,`, and `/review. ` because a dot before a
        // non-token character is sentence punctuation). Only text that would
        // join the reference (or an empty suffix) gets a space.
        const separator = suffix !== "" && completeSkillReferenceAt(`/${name}${suffix}`, 0, [name]) === name ? "" : " ";
        const content = [skillSchema.nodes.skill.create({ name })];
        if (separator) content.push(skillSchema.text(separator));
        const inserted = content.reduce((size, node) => size + node.nodeSize, 0);
        const tr = closeHistory(view.state.tr.replaceWith(from, to, content));
        tr.setSelection(TextSelection.create(tr.doc, from + inserted));
        view.dispatch(tr);
        view.focus();
        return true;
      },
    }),
    [],
  );

  useLayoutEffect(() => {
    if (!host.current) return;
    const view = new EditorView(
      { mount: host.current },
      {
        state: createState(latest.current.value),
        dispatchTransaction: (transaction) => {
          view.updateState(view.state.apply(transaction));
          view.dom.dataset.empty = String(view.state.doc.content.size === 0);
          // An externally applied value is the controlled value arriving, not
          // the user editing: echoing it back would re-enter the composer's own
          // edit path during its submission and recovery transitions.
          if (transaction.docChanged && !transaction.getMeta(externalSync)) {
            latest.current.onChange(
              serializeSkillDocument(view.state.doc),
              documentPositionToTextOffset(view.state.doc, view.state.selection.head),
            );
          }
        },
        nodeViews: {
          skill: (node) => {
            const dom = document.createElement("span");
            dom.className = styles.skill ?? "";
            dom.dataset.skillName = node.attrs.name;
            dom.dataset.testid = "composer-skill-chip";
            dom.setAttribute("contenteditable", "false");
            dom.setAttribute("role", "note");
            dom.textContent = `/${node.attrs.name}`;
            updateDetails(dom, node.attrs.name, latest.current);
            return { dom };
          },
        },
        // Never parse clipboard HTML, even if it contains our data attributes.
        handlePaste: (_view, event) => {
          const text = event.clipboardData?.getData("text/plain").replace(/\r\n?/g, "\n");
          if (text)
            view.dispatch(
              closeHistory(view.state.tr.insertText(text)).setMeta("paste", true).setMeta("uiEvent", "paste"),
            );
          return true;
        },
        clipboardTextSerializer: (slice) => slice.content.textBetween(0, slice.content.size, ""),
      },
    );
    viewRef.current = view;
    return () => {
      viewRef.current = null;
      view.destroy();
    };
  }, []);

  useLayoutEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    const current = serializeSkillDocument(view.state.doc);
    // Compare the serialized boundary BEFORE parsing. A plain pasted /name must
    // not become an atom just because an earlier mention selected that name.
    const echo =
      current.text === props.value.text &&
      current.skillNames.length === props.value.skillNames.length &&
      current.skillNames.every((name, index) => props.value.skillNames[index] === name);
    // Consumed on every pass, even when the value already matches: a restore
    // whose value happens to equal what is on screen is still spent, and leaving
    // it pending would make the next ordinary patch look authoritative.
    const authoritative = (props.restoreEpoch ?? 0) !== appliedRestoreRef.current;
    appliedRestoreRef.current = props.restoreEpoch ?? 0;
    if (!echo) {
      // Applied through a transaction, never by rebuilding the state: a fresh
      // EditorState re-initializes every plugin, so one attachment marker would
      // end the session's undo history. addToHistory: false, because the change
      // came from the controlled value rather than the keyboard - undoing a
      // cleared submission must not put the sent message back.
      const apply = (transaction: typeof view.state.tr) =>
        view.dispatch(transaction.setMeta("addToHistory", false).setMeta(externalSync, true));
      const current = serializeSkillDocument(view.state.doc).text;
      const prefix = sharedPrefixLength(current, props.value.text);
      const end = current.length - sharedSuffixLength(current, props.value.text, prefix);
      // A boundary strictly inside an atom cannot be mapped to a position that
      // replaces that atom, and the value as a whole is authoritative whenever
      // the caller marked it a restore or it replaces everything - a goal
      // command, a cleared submission. Both cases rebuild from the value; only
      // a partial, unmarked change is a patch, and what it inserts is plain
      // text: a spliced command or quote that happens to spell a selected name
      // must not become an activation the user never chose.
      const splitsAtom = atomInteriorAt(view.state.doc, prefix) || atomInteriorAt(view.state.doc, end);
      if (authoritative || splitsAtom || (prefix === 0 && end === current.length)) {
        const replacement = parseSkillDocument(props.value);
        if (!replacement.eq(view.state.doc))
          apply(view.state.tr.replaceWith(0, view.state.doc.content.size, replacement.content));
      } else {
        const from = textOffsetToDocumentPosition(view.state.doc, prefix, 1);
        const to = textOffsetToDocumentPosition(view.state.doc, end, -1);
        const inserted = props.value.text.slice(prefix, props.value.text.length - (current.length - end));
        apply(view.state.tr.replaceWith(from, to, inserted ? skillSchema.text(inserted) : Fragment.empty));
      }
      // The applied value is not the last word: the integrity plugin separates
      // a chip from a token this change just joined to it, and the echo is
      // suppressed for an external value. Reporting what the document actually
      // became keeps the composer's text, its draft and the screen saying the
      // same thing.
      const settled = serializeSkillDocument(view.state.doc);
      if (
        settled.text !== props.value.text ||
        settled.skillNames.length !== props.value.skillNames.length ||
        !settled.skillNames.every((name, index) => props.value.skillNames[index] === name)
      ) {
        latest.current.onChange(settled, documentPositionToTextOffset(view.state.doc, view.state.selection.head));
      }
    }
    view.setProps({
      attributes: {
        class: styles.editor ?? "",
        role: "textbox",
        "aria-multiline": "true",
        "aria-label": props["aria-label"] ?? "Message",
        ...(props["aria-controls"] ? { "aria-controls": props["aria-controls"] } : {}),
        ...(props["aria-activedescendant"] ? { "aria-activedescendant": props["aria-activedescendant"] } : {}),
        "data-placeholder": props.placeholder ?? "",
        "data-empty": String(view.state.doc.content.size === 0),
      },
    });
    view.dom.style.minHeight = `${props.minLines ?? 2}lh`;
    for (const dom of view.dom.querySelectorAll<HTMLElement>("[data-skill-name]")) {
      updateDetails(dom, dom.dataset.skillName ?? "", props);
    }
  });

  return (
    // biome-ignore lint/a11y/useSemanticElements: Native text inputs cannot contain ProseMirror's indivisible inline skill nodes.
    <div
      ref={host}
      className={styles.editor}
      role="textbox"
      tabIndex={0}
      contentEditable
      aria-multiline="true"
      onKeyDownCapture={(event) => {
        if (!viewRef.current?.composing && !event.nativeEvent.isComposing && event.keyCode !== 229)
          props.onKeyDown?.(event);
      }}
      onPasteCapture={(event) => {
        // Attachment markers and their restored cursor must reach the view
        // before its native paste handler inserts the text portion.
        flushSync(() => props.onPaste?.(event));
      }}
      onFocus={props.onFocus}
      onBlur={props.onBlur}
    />
  );
});
