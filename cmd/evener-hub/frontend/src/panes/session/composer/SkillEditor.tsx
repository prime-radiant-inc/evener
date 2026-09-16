import { baseKeymap, deleteSelection } from "prosemirror-commands";
import { closeHistory, history, redo, undo } from "prosemirror-history";
import { keymap } from "prosemirror-keymap";
import { type Command, EditorState, TextSelection } from "prosemirror-state";
import { EditorView } from "prosemirror-view";
import { forwardRef, type HTMLAttributes, useImperativeHandle, useLayoutEffect, useRef } from "react";
import { flushSync } from "react-dom";
import {
  documentPositionToTextOffset,
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
  insertSkill(start: number, end: number, name: string): void;
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

function createState(value: SkillEditorValue): EditorState {
  const newline: Command = (state, dispatch) => {
    dispatch?.(state.tr.insertText("\n"));
    return true;
  };
  return EditorState.create({
    doc: parseSkillDocument(value),
    plugins: [
      history(),
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
  useLayoutEffect(() => {
    latest.current = props;
  });

  useImperativeHandle(
    ref,
    () => ({
      focus: () => viewRef.current?.focus(),
      getCursor: () => {
        const view = viewRef.current;
        return view ? documentPositionToTextOffset(view.state.doc, view.state.selection.head) : 0;
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
        if (!view || view.composing) return;
        const { doc } = view.state;
        const from = textOffsetToDocumentPosition(doc, start);
        const to = textOffsetToDocumentPosition(doc, end, 1);
        const suffix = doc.textBetween(to, doc.content.size);
        const separator = /^\s/.test(suffix) ? "" : " ";
        const content = [skillSchema.nodes.skill.create({ name })];
        if (separator) content.push(skillSchema.text(separator));
        const tr = closeHistory(view.state.tr.replaceWith(from, to, content));
        tr.setSelection(TextSelection.create(tr.doc, from + content.length));
        view.dispatch(tr);
        view.focus();
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
          if (transaction.docChanged) {
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
    if (!echo) {
      const replacement = parseSkillDocument(props.value);
      if (!replacement.eq(view.state.doc))
        view.updateState(EditorState.create({ doc: replacement, plugins: view.state.plugins }));
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
