import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { createRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import "../testing/editorGeometry";
import { createBrowserDraftRepository } from "../../../stores/browserDraftRepository";
import { createDocumentAnnotationRepository } from "../../../stores/documentAnnotationIndexedDB";
import {
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
} from "../../../stores/localCompositionSchema";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { REFERENCE } from "../../doc/testing/annotationFixtures";
import {
  acceptedOperation,
  attachmentIdentity,
  blobBytes,
  cleanupAfterTest,
  fixture,
  rowsForTests,
} from "../../doc/testing/annotationRepositoryFixtures";
import { SkillEditor, type SkillEditorHandle, type SkillEditorProps, type SkillEditorValue } from "./SkillEditor";
import { type ComposerSourceSnapshot, createComposerSourceState } from "./sourceState";

function mount(initial: SkillEditorValue, extra: Partial<SkillEditorProps> = {}) {
  const ref = createRef<SkillEditorHandle>();
  const changed = vi.fn();
  let current = initial;
  function Controlled() {
    const [value, setValue] = useState(initial);
    return (
      <SkillEditor
        ref={ref}
        value={value}
        aria-label="Message"
        {...extra}
        onChange={(next, cursor) => {
          current = next;
          changed(next, cursor);
          setValue(next);
        }}
      />
    );
  }
  const result = render(<Controlled />);
  const editor = result.getByRole("textbox");
  return { ...result, editor, ref, changed, value: () => current };
}

function paste(editor: HTMLElement, text: string, html = "") {
  const clipboardData = {
    getData: (type: string) => (type === "text/plain" ? text : type === "text/html" ? html : ""),
    types: ["text/plain", "text/html"],
    files: [],
    items: [],
  };
  fireEvent.paste(editor, { clipboardData });
}

function key(editor: HTMLElement, key: string, extra: Record<string, unknown> = {}) {
  fireEvent.keyDown(editor, { key, ...extra });
}

describe("SkillEditor real ProseMirror view", () => {
  it("keeps same-spelling command and skill atoms distinct through paste, remount and undo", () => {
    const result = mount({ text: "Use /same and /sa", skillNames: ["same"] });
    act(() => result.ref.current?.insertCommand(14, 17, "same"));
    expect(result.value().commandNames).toEqual(["same"]);
    expect(result.value().skillNames).toEqual(["same"]);
    paste(result.editor, "/same");
    expect(result.editor.querySelectorAll("[data-command-name]")).toHaveLength(1);
    expect(result.editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    const saved = JSON.parse(JSON.stringify(result.value()));
    result.unmount();
    const restored = mount(saved);
    expect(restored.editor.querySelectorAll("[data-command-name]")).toHaveLength(1);
    expect(restored.editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    act(() => restored.ref.current?.setSelection(19));
    key(restored.editor, "Backspace");
    expect(restored.value().commandNames ?? []).toEqual([]);
    expect(restored.value().skillNames).toEqual(["same"]);
    key(restored.editor, "z", { ctrlKey: true });
    expect(restored.value().commandNames).toEqual(["same"]);
  });

  it("keeps pasted same-spelling prose inert after the last command is deleted and the draft remounts", () => {
    const result = mount({
      text: "/same and /same and /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "skill", name: "same", offset: 0 },
        { kind: "command", name: "same", offset: 10 },
      ],
    });
    act(() => result.ref.current?.setSelection(10, 15));
    key(result.editor, "Delete");
    expect(result.value().text).toBe("/same and  and /same");
    const saved = JSON.parse(JSON.stringify(result.value()));
    result.unmount();
    const restored = mount(saved);
    expect(restored.editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    expect(restored.editor.querySelectorAll("[data-command-name]")).toHaveLength(0);
  });

  it("replaces a completion range with an inline atom and preserves controlled-echo cursor/focus", () => {
    const result = mount(
      { text: "Use /rev now", skillNames: [] },
      { skillDetails: () => "Review code; source: project" },
    );
    act(() => {
      result.ref.current?.focus();
      result.ref.current?.insertSkill(4, 8, "team:review");
    });
    expect(result.value()).toEqual({ text: "Use /team:review now", skillNames: ["team:review"] });
    expect(result.ref.current?.getCursor()).toBe("Use /team:review".length);
    expect(document.activeElement).toBe(result.editor);
    const atom = result.editor.querySelector('[data-skill-name="team:review"]');
    expect(atom?.textContent).toBe("/team:review");
    expect(atom?.getAttribute("contenteditable")).toBe("false");
    expect(atom?.getAttribute("title")).toBe("Review code; source: project");
    expect(atom?.getAttribute("aria-label")).toContain("Review code; source: project");
  });

  it.each(["Backspace", "Delete"])(
    "%s removes one adjacent atom in one keypress and undo/redo restores metadata",
    (direction) => {
      const result = mount({ text: "a /review b", skillNames: ["review"] });
      act(() => result.ref.current?.setSelection(direction === "Backspace" ? 9 : 2));
      key(result.editor, direction);
      expect(result.value()).toEqual({ text: "a  b", skillNames: [] });
      key(result.editor, "z", { ctrlKey: true });
      expect(result.value()).toEqual({ text: "a /review b", skillNames: ["review"] });
      key(result.editor, "z", { ctrlKey: true, shiftKey: true });
      expect(result.value()).toEqual({ text: "a  b", skillNames: [] });
    },
  );

  it.each(["", "suffix", " suffix", "\nsuffix"])(
    "inserts the completion separator in one undoable transaction before %j",
    (suffix) => {
      const result = mount({ text: `Run /rev${suffix}`, skillNames: [] });
      act(() => result.ref.current?.insertSkill(4, 8, "review"));
      const separator = /^\s/.test(suffix) ? "" : " ";
      expect(result.value()).toEqual({ text: `Run /review${separator}${suffix}`, skillNames: ["review"] });
      expect(result.ref.current?.getCursor()).toBe(`Run /review${separator}`.length);
      key(result.editor, "z", { ctrlKey: true });
      expect(result.value()).toEqual({ text: `Run /rev${suffix}`, skillNames: [] });
      key(result.editor, "z", { ctrlKey: true, shiftKey: true });
      expect(result.value()).toEqual({ text: `Run /review${separator}${suffix}`, skillNames: ["review"] });
    },
  );

  it("replaces a selection including skills with plain text, and undo restores all mentions", () => {
    const result = mount({ text: "/review and /review", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(0, 7));
    paste(result.editor, "first");
    expect(result.value()).toEqual({ text: "first and /review", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(0, result.value().text.length));
    paste(result.editor, "replacement");
    expect(result.value()).toEqual({ text: "replacement", skillNames: [] });
    key(result.editor, "z", { ctrlKey: true });
    expect(result.value()).toEqual({ text: "first and /review", skillNames: ["review"] });
  });

  it("pastes literal multiline text, rejects rich formatting, and never activates copied slash text", () => {
    const result = mount({ text: "", skillNames: [] });
    paste(
      result.editor,
      "one\ntwo\n/review",
      '<b>one</b><script>alert(1)</script><span data-skill-name="review">/review</span>',
    );
    expect(result.value()).toEqual({ text: "one\ntwo\n/review", skillNames: [] });
    expect(result.editor.querySelector("b,script,[data-skill-name]")).toBeNull();
    key(result.editor, "Enter", { shiftKey: true });
    expect(result.value().text).toBe("one\ntwo\n/review\n");
  });

  it("preserves newlines when the browser mutates a text node while typing", async () => {
    const result = mount({ text: "first\nsecond", skillNames: [] });
    await act(async () => {
      const text = result.editor.firstChild;
      if (!(text instanceof Text)) throw new Error("Expected a real editor text node");
      text.appendData("!");
    });
    await waitFor(() => expect(result.value()).toEqual({ text: "first\nsecond!", skillNames: [] }));
  });

  it("keeps pasted duplicate slash text plain through controlled echoes and deduplicates only actual atoms", () => {
    const result = mount({ text: "/review and ", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(result.value().text.length));
    paste(result.editor, "/review");
    expect(result.value()).toEqual({
      text: "/review and /review",
      skillNames: ["review"],
      mentions: [{ kind: "skill", name: "review", offset: 0 }],
    });
    expect(result.editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    act(() => result.ref.current?.setSelection(0, 7));
    key(result.editor, "Delete");
    expect(result.value()).toEqual({ text: " and /review", skillNames: [] });
  });

  it("maps selection around emoji, newlines and indivisible atoms", () => {
    const result = mount({ text: "😀\n/review\nlast", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(10));
    expect(result.ref.current?.getCursor()).toBe(10);
    act(() => result.ref.current?.setSelection(5, 8));
    paste(result.editor, "replacement");
    expect(result.value()).toEqual({ text: "😀\nreplacement\nlast", skillNames: [] });
  });

  it("runs React keyboard/paste callbacks before editor defaults, including mixed image/text paste", () => {
    let allow = false;
    const onKeyDown = vi.fn((event) => {
      if (!allow) event.preventDefault();
    });
    const onPaste = vi.fn((event) => {
      if (!allow) event.preventDefault();
    });
    const result = mount({ text: "", skillNames: [] }, { onKeyDown, onPaste });
    key(result.editor, "Enter");
    paste(result.editor, "blocked");
    expect(result.value().text).toBe("");
    allow = true;
    key(result.editor, "Enter");
    fireEvent.paste(result.editor, {
      clipboardData: {
        getData: (type: string) => (type === "text/plain" ? "caption" : ""),
        types: ["text/plain", "Files"],
        files: [new File(["image"], "image.png", { type: "image/png" })],
        items: [],
      },
    });
    expect(onKeyDown).toHaveBeenCalledTimes(2);
    expect(onPaste).toHaveBeenCalledTimes(2);
    expect(onPaste.mock.calls[1]?.[0].clipboardData.files).toHaveLength(1);
    expect(result.value().text).toBe("\ncaption");
  });

  it("does not separate a completed skill from punctuation that already bounds it", () => {
    const result = mount({ text: "/rev, please", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    // The comma already bounds the reference, so no separator is needed; adding
    // one would put whitespace in the request the user never typed.
    expect(result.value()).toEqual({ text: "/review, please", skillNames: ["review"] });
  });

  it("does not separate a completed skill from sentence punctuation", () => {
    const result = mount({ text: "/rev. more", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    // A dot followed by a non-token character already bounds the reference, so
    // the parser reads `/review. more` as complete and no space is needed.
    expect(result.value()).toEqual({ text: "/review. more", skillNames: ["review"] });
  });

  it("separates a completed skill from text that would join its reference", () => {
    const result = mount({ text: "/revplease", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    expect(result.value()).toEqual({ text: "/review please", skillNames: ["review"] });
  });

  it("reports a non-collapsed selection's lower offset, as the textarea's selectionStart did", () => {
    const result = mount({ text: "keep SELECTED tail", skillNames: [] });
    act(() => result.ref.current?.setSelection(5, 13));
    expect(result.ref.current?.getSelection()).toEqual({ start: 5, end: 13 });
    // `getCursor` is the anchor attachment marker stripping uses, and the field
    // it replaced reported the selection's start - not its focus end.
    expect(result.ref.current?.getCursor()).toBe(5);
  });

  it("does not submit Enter or insert a newline while composing", () => {
    const onKeyDown = vi.fn();
    const result = mount({ text: "", skillNames: [] }, { onKeyDown });
    fireEvent.compositionStart(result.editor);
    key(result.editor, "Enter", { isComposing: true, keyCode: 229 });
    expect(onKeyDown).not.toHaveBeenCalled();
    expect(result.value().text).toBe("");
    fireEvent.compositionEnd(result.editor);
  });

  it.each(["skill", "command"] as const)("refuses %s atom completion during IME without changing prose", (kind) => {
    const result = mount({ text: "Use /rev", skillNames: [] });
    fireEvent.compositionStart(result.editor);
    let inserted = true;
    act(() => {
      inserted =
        kind === "skill"
          ? result.ref.current!.insertSkill(4, 8, "review")
          : result.ref.current!.insertCommand(4, 8, "review");
    });
    expect(inserted).toBe(false);
    expect(result.value()).toEqual({ text: "Use /rev", skillNames: [] });
    expect(result.editor.querySelector("[contenteditable='false']")).toBeNull();
    fireEvent.compositionEnd(result.editor);
  });

  it("reconciles external replacement and resets history so cleared submissions cannot reappear", () => {
    const ref = createRef<SkillEditorHandle>();
    const changed = vi.fn();
    const result = render(
      <SkillEditor ref={ref} value={{ text: "/rev", skillNames: [] }} onChange={changed} aria-label="Message" />,
    );
    const editor = result.getByRole("textbox");
    act(() => ref.current?.insertSkill(0, 4, "review"));
    const echo = { text: "/review ", skillNames: ["review"] };
    result.rerender(<SkillEditor ref={ref} value={echo} onChange={changed} aria-label="Message" />);
    expect(ref.current?.getCursor()).toBe(8);
    key(editor, "z", { ctrlKey: true });
    expect(changed.mock.lastCall?.[0]).toEqual({ text: "/rev", skillNames: [] });
    result.rerender(
      <SkillEditor ref={ref} value={{ text: "", skillNames: [] }} onChange={changed} aria-label="Message" />,
    );
    changed.mockClear();
    key(editor, "z", { ctrlKey: true });
    expect(editor.textContent).toBe("");
    expect(changed).not.toHaveBeenCalled();
    result.rerender(
      <SkillEditor
        ref={ref}
        value={{ text: "/reviewer /review", skillNames: ["review", "invisible"] }}
        onChange={changed}
        aria-label="Message"
      />,
    );
    expect(editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
  });

  it("serializes native clipboard selection with names and preserves metadata on undo after cut", () => {
    const result = mount({ text: "Use /review\nnow", skillNames: ["review"] });
    act(() => {
      result.ref.current?.focus();
      result.ref.current?.setSelection(0, result.value().text.length);
    });
    const setData = vi.fn();
    const clipboardData = { clearData: vi.fn(), setData };
    fireEvent.copy(result.editor, { clipboardData });
    expect(setData).toHaveBeenCalledWith("text/plain", "Use /review\nnow");
    fireEvent.cut(result.editor, { clipboardData });
    expect(result.value()).toEqual({ text: "", skillNames: [] });
    key(result.editor, "z", { ctrlKey: true });
    expect(result.value()).toEqual({ text: "Use /review\nnow", skillNames: ["review"] });
  });

  it("forwards accessible attributes, placeholder, focus/blur, and updates detail callbacks", () => {
    const ref = createRef<SkillEditorHandle>();
    const onFocus = vi.fn();
    const onBlur = vi.fn();
    const props = {
      value: { text: "/review", skillNames: ["review"] },
      onChange: vi.fn(),
      onFocus,
      onBlur,
      "aria-label": "Message",
      "aria-controls": "skills",
      "aria-activedescendant": "review-option",
      placeholder: "Write",
      minLines: 3,
    };
    const result = render(<SkillEditor ref={ref} {...props} skillDetails={() => "Old"} />);
    const editor = result.getByRole("textbox");
    expect(editor.getAttribute("aria-multiline")).toBe("true");
    expect(editor.getAttribute("aria-controls")).toBe("skills");
    expect(editor.getAttribute("aria-activedescendant")).toBe("review-option");
    expect(editor.getAttribute("data-placeholder")).toBe("Write");
    act(() => ref.current?.focus());
    fireEvent.blur(editor);
    expect(onFocus).toHaveBeenCalled();
    expect(onBlur).toHaveBeenCalled();
    result.rerender(<SkillEditor ref={ref} {...props} skillDetails={() => "New"} />);
    expect(editor.querySelector("[data-skill-name]")?.getAttribute("title")).toBe("New");
  });
});

describe("SkillEditor annotation reference through remount and persisted reload", () => {
  // Expected compositions are declared independently of the source that produced
  // them; the fixture's same/plan offsets are the literal 9.
  function expectedDraftValue(): SkillEditorValue {
    return {
      text: `ordinary /same\n\n${REFERENCE}`,
      skillNames: ["same"],
      mentions: [{ kind: "skill", name: "same", offset: 9 }],
    };
  }

  function expectedRecoveryValue(): SkillEditorValue {
    return {
      text: "recovery /plan [image 1]",
      skillNames: [],
      commandNames: ["plan"],
      mentions: [{ kind: "command", name: "plan", offset: 9 }],
    };
  }

  function sourceValue(snapshot: ComposerSourceSnapshot): SkillEditorValue {
    return {
      text: snapshot.text,
      skillNames: [...snapshot.skillNames],
      ...(snapshot.commandNames.length ? { commandNames: [...snapshot.commandNames] } : {}),
      ...(snapshot.mentions ? { mentions: snapshot.mentions.map((mention) => ({ ...mention })) } : {}),
    };
  }

  function unmountOnce(result: { unmount: () => void }): () => void {
    let done = false;
    const run = () => {
      if (done) return;
      done = true;
      result.unmount();
    };
    cleanupAfterTest(run);
    return run;
  }

  it.each(["draft", "recovery"] as const)(
    "keeps an appended annotation reference literal through editor remount and persisted reload on %s",
    async (backing) => {
      const f = await fixture(`editor-${backing}`);
      // Settle the fixture's earlier pending op so this session's FIFO is free.
      expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
      if (backing === "recovery") {
        f.source.setActiveRecoveryId("rec-1");
        await f.source.prepareAnnotation();
      }
      const expectedOriginal = backing === "draft" ? expectedDraftValue() : expectedRecoveryValue();
      // Preserve the real source boundary: mount the ACTUAL source value below.
      const originalValue = sourceValue(f.source.getSnapshot());
      expect(originalValue).toEqual(expectedOriginal);

      const note = backing === "draft" ? "see /same here" : "see /plan here";
      const reference = REFERENCE.replace('"Keep this scope."', `"${note}"`);
      const expectedText = `${expectedOriginal.text}\n\n${reference}`;

      const ordinaryBefore = await f.drafts.load(f.key);
      const recoveryBefore = await f.outbox.getRecovery("rec-1");
      if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
      const recoveryAttachmentBefore = recoveryBefore.attachments[0];
      if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
      expect(recoveryAttachmentBefore).toMatchObject(attachmentIdentity);
      // The independently declared fixture byte, not a source-produced oracle.
      expect(await blobBytes(recoveryAttachmentBefore.blob)).toEqual(new Uint8Array([1]));

      // Independent baseline identities for the full applied receipt and payload.
      const origin = location.origin;
      const operationKey = JSON.stringify(["browser", origin, `editor-${backing}`]);
      const documentKey = JSON.stringify(["browser", origin, "root", "NOTES.md"]);
      const namespaceKey = JSON.stringify(["browser", origin]);
      const sessionKey = JSON.stringify(["browser", origin, "root"]);
      const opOnePayload = (
        await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)
      ).find((row) => row.operation.operationId === "op-1");
      const opOneReceipt = (
        await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
      ).find((row) => row.operationId === "op-1");
      if (!opOnePayload || !opOneReceipt) throw new Error("settled fixture baseline missing");
      if (opOneReceipt.state !== "applied") throw new Error("fixture op-1 is not applied");
      const epoch = opOnePayload.epoch;
      const advancedToken = {
        ...ordinaryBefore.token,
        sessionRevision: ordinaryBefore.token.sessionRevision + 1,
      };
      const expectedReceipt = {
        operationKey,
        namespaceKey,
        namespaceIncarnation: opOneReceipt.namespaceIncarnation,
        documentKey,
        sessionKey,
        sessionSequence: opOneReceipt.sessionSequence + 1,
        operationId: `editor-${backing}`,
        epoch,
        state: "applied" as const,
        draftRevision: ordinaryBefore.token.sessionRevision + 1,
      };
      const expectedRecoveryInput = [
        { type: "text", text: expectedText, mentions: [{ kind: "command", name: "plan", offset: 9 }] },
        { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
        { type: "command", name: "plan" },
      ];
      const expectedRecoveryAfter = {
        ...recoveryBefore,
        composerText: expectedText,
        composerMentions: [{ kind: "command", name: "plan", offset: 9 }],
        payload: { ...recoveryBefore.payload, input: expectedRecoveryInput },
        optimisticDisplay: { method: "turn/start", input: expectedRecoveryInput },
      };

      const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
      const resends = vi.spyOn(f.outbox, "resendRecovery");
      const recoveryEdits = vi.spyOn(f.outbox, "updateRecoveryInput");

      // Mount the current composition, then unmount ONLY the editor; the source
      // and its retained value survive.
      const first = mount(originalValue);
      const unmountFirst = unmountOnce(first);
      expect(first.editor.textContent).toBe(originalValue.text);
      unmountFirst();

      const operation = await acceptedOperation(f.repo, f.document, `editor-${backing}`, note, f.destination);
      await f.repo.saveAnnotation(operation);
      expect((await f.repo.handoff(f.document, operation.operationId)).state).toBe("applied");
      if (backing === "recovery") {
        f.source.setActiveRecoveryId("rec-1");
        await f.source.prepareAnnotation();
      }
      const expectedPayload = { payloadKey: operationKey, documentKey, epoch, operation };

      const afterValue = sourceValue(f.source.getSnapshot());
      expect(afterValue).toEqual({ ...expectedOriginal, text: expectedText });

      const skillAtoms = backing === "draft" ? 1 : 0;
      const commandAtoms = backing === "draft" ? 0 : 1;
      const second = mount(afterValue);
      const unmountSecond = unmountOnce(second);
      expect(second.editor.textContent).toBe(expectedText);
      expect(second.editor.querySelectorAll("[data-skill-name]")).toHaveLength(skillAtoms);
      expect(second.editor.querySelectorAll("[data-command-name]")).toHaveLength(commandAtoms);
      unmountSecond();

      // The annotation caused no local send/resend/update and left the outbox empty.
      expect(enqueues).not.toHaveBeenCalled();
      expect(resends).not.toHaveBeenCalled();
      expect(recoveryEdits).not.toHaveBeenCalled();
      expect(await f.outbox.listOutbox()).toEqual([]);

      // Persisted reload: dispose the source, close the original repositories and
      // reopen fresh ones on the SAME real factory/database, replaying through a
      // recreated annotation adapter.
      await f.source.whenSettled();
      f.source.dispose();
      f.repo.close();
      f.drafts.close();
      f.outbox.close();
      const reopenedDrafts = createBrowserDraftRepository({ indexedDB: f.factory, databaseName: f.databaseName });
      cleanupAfterTest(() => reopenedDrafts.close());
      const reopenedOutbox = new MutationOutboxIndexedDB({
        indexedDB: f.factory,
        databaseName: f.databaseName,
        createMutationId: () => "rec-1",
        createPresentationId: () => "image-1",
        now: () => 100,
      });
      cleanupAfterTest(() => reopenedOutbox.close());
      const reopenedRepo = createDocumentAnnotationRepository({
        indexedDB: f.factory,
        databaseName: f.databaseName,
        drafts: reopenedDrafts,
        targetFor: () => null,
      });
      cleanupAfterTest(() => reopenedRepo.close());
      const reopenedEnqueues = vi.spyOn(reopenedOutbox, "enqueueIntent");
      const reopenedResends = vi.spyOn(reopenedOutbox, "resendRecovery");
      const reopenedEdits = vi.spyOn(reopenedOutbox, "updateRecoveryInput");
      const freshSource = createComposerSourceState("root", {
        repository: reopenedDrafts,
        key: f.key,
        outbox: reopenedOutbox,
      });
      cleanupAfterTest(async () => {
        await freshSource.whenSettled();
        freshSource.dispose();
      });
      await freshSource.ready();
      if (backing === "recovery") {
        freshSource.setActiveRecoveryId("rec-1");
        await freshSource.prepareAnnotation();
      }
      const reloadedValue = sourceValue(freshSource.getSnapshot());
      expect(reloadedValue).toEqual(afterValue);
      const reloaded = mount(reloadedValue);
      const unmountReloaded = unmountOnce(reloaded);
      expect(reloaded.editor.textContent).toBe(expectedText);
      expect(reloaded.editor.querySelectorAll("[data-skill-name]")).toHaveLength(skillAtoms);
      expect(reloaded.editor.querySelectorAll("[data-command-name]")).toHaveLength(commandAtoms);
      unmountReloaded();

      // Read BOTH backings immediately before replay and assert their complete,
      // independently expected values plus the literal fixture byte.
      const ordinaryBeforeReplay = await reopenedDrafts.load(f.key);
      const recoveryBeforeReplay = await reopenedOutbox.getRecovery("rec-1");
      if (!ordinaryBeforeReplay || !recoveryBeforeReplay) throw new Error("prereplay rows missing");
      if (backing === "draft") {
        expect(ordinaryBeforeReplay).toEqual({ ...ordinaryBefore, text: expectedText, token: advancedToken });
        expect(recoveryBeforeReplay).toEqual(recoveryBefore);
      } else {
        expect(ordinaryBeforeReplay).toEqual({ ...ordinaryBefore, token: advancedToken });
        expect(recoveryBeforeReplay).toEqual(expectedRecoveryAfter);
      }
      const recoveryAttachmentBeforeReplay = recoveryBeforeReplay.attachments[0];
      if (!recoveryAttachmentBeforeReplay) throw new Error("prereplay recovery attachment missing");
      expect(recoveryAttachmentBeforeReplay).toMatchObject(attachmentIdentity);
      expect(await blobBytes(recoveryAttachmentBeforeReplay.blob)).toEqual(new Uint8Array([1]));

      const payloadsBeforeReplay = await rowsForTests<AnnotationPayloadRow>(
        f.factory,
        f.databaseName,
        ANNOTATION_PAYLOADS_STORE,
      );
      expect(payloadsBeforeReplay.filter((row) => row.operation.operationId === operation.operationId)).toEqual([
        expectedPayload,
      ]);
      const receiptsBeforeReplay = await rowsForTests<AnnotationOperationRow>(
        f.factory,
        f.databaseName,
        ANNOTATION_OPERATIONS_STORE,
      );
      expect(receiptsBeforeReplay.find((row) => row.operationId === operation.operationId)).toEqual(expectedReceipt);

      // Terminal replay cannot add a reference or change any row.
      expect((await reopenedRepo.handoff(f.document, operation.operationId)).state).toBe("already-applied");

      // Freshly reread BOTH backings and compare to the prereplay expectations.
      const ordinaryAfter = await reopenedDrafts.load(f.key);
      const recoveryAfter = await reopenedOutbox.getRecovery("rec-1");
      if (!ordinaryAfter || !recoveryAfter) throw new Error("post-reload rows missing");
      expect(ordinaryAfter).toEqual(ordinaryBeforeReplay);
      expect(recoveryAfter).toEqual(recoveryBeforeReplay);
      const recoveryAttachmentAfter = recoveryAfter.attachments[0];
      if (!recoveryAttachmentAfter) throw new Error("post-reload recovery attachment missing");
      expect(recoveryAttachmentAfter).toMatchObject(attachmentIdentity);
      expect(await blobBytes(recoveryAttachmentAfter.blob)).toEqual(new Uint8Array([1]));

      const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
      expect(payloads.filter((row) => row.operation.operationId === operation.operationId)).toEqual([expectedPayload]);
      const receipts = await rowsForTests<AnnotationOperationRow>(
        f.factory,
        f.databaseName,
        ANNOTATION_OPERATIONS_STORE,
      );
      expect(receipts.find((row) => row.operationId === operation.operationId)).toEqual(expectedReceipt);

      expect(reopenedEnqueues).not.toHaveBeenCalled();
      expect(reopenedResends).not.toHaveBeenCalled();
      expect(reopenedEdits).not.toHaveBeenCalled();
      expect(await reopenedOutbox.listOutbox()).toEqual([]);
    },
  );
});
