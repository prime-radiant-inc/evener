import { MAX_ATTACHMENT_BYTES, MAX_SOURCE_BYTES, MAX_SOURCE_PIXELS } from "@evener/appwire-client";
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import * as ImageEncoding from "./encodeImage";
import { type TextEditor, useAttachments } from "./useAttachments";

// Same jsdom canvas-stub technique as encodeImage.test.ts (see that file's
// own header comment) - useAttachments calls the REAL encodeImage
// internally, so its tests exercise the real orchestration end to end
// rather than mocking the module it depends on.
const realEncodeImage = ImageEncoding.encodeImage;
let originalGetContext: typeof HTMLCanvasElement.prototype.getContext;
let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob;
let originalImage: typeof Image;
let originalCreateObjectURL: typeof URL.createObjectURL;
let originalRevokeObjectURL: typeof URL.revokeObjectURL;
let decodes: Promise<void>[];
let pendingEncodes: Promise<unknown>[];
let encodeSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  decodes = [];
  pendingEncodes = [];
  encodeSpy = vi.spyOn(ImageEncoding, "encodeImage").mockImplementation((blob) => {
    const completion = realEncodeImage(blob);
    pendingEncodes.push(completion);
    return completion;
  });
  originalGetContext = HTMLCanvasElement.prototype.getContext;
  originalToBlob = HTMLCanvasElement.prototype.toBlob;
  originalImage = globalThis.Image;
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};

  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
    fillRect() {},
  })) as unknown as typeof originalGetContext;
  HTMLCanvasElement.prototype.toBlob = function (this: HTMLCanvasElement, callback: BlobCallback, type?: string): void {
    callback(new Blob([new Uint8Array([1, 2, 3])], { type: type ?? "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 8;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      decodes.push(Promise.resolve().then(() => this.onload?.()));
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
});

afterEach(() => {
  HTMLCanvasElement.prototype.getContext = originalGetContext;
  HTMLCanvasElement.prototype.toBlob = originalToBlob;
  globalThis.Image = originalImage;
  URL.createObjectURL = originalCreateObjectURL;
  URL.revokeObjectURL = originalRevokeObjectURL;
  vi.restoreAllMocks();
});

function installImageDimensionsStub(width: number, height: number): void {
  class SizedImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    naturalWidth = width;
    naturalHeight = height;
    width = width;
    height = height;
    private _src = "";
    set src(value: string) {
      this._src = value;
      decodes.push(Promise.resolve().then(() => this.onload?.()));
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = SizedImage;
}

function installDecodeErrorStub(): void {
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
}

function installControlledImageStub(): {
  resolve(index?: number): Promise<void>;
  reject(index?: number): Promise<void>;
  count(): number;
} {
  const images: ControlledImage[] = [];
  const baseEncodeIndex = pendingEncodes.length;
  class ControlledImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 8;
    height = 4;
    src = "";

    constructor() {
      images.push(this);
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = ControlledImage;
  return {
    count: () => images.length,
    resolve: (index = images.length - 1) => {
      const image = images[index];
      if (!image) throw new Error(`No controlled image at ${index}`);
      const completion = pendingEncodes[baseEncodeIndex + index];
      image.onload?.();
      return Promise.resolve(completion).then(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
    },
    reject: (index = images.length - 1) => {
      const image = images[index];
      if (!image) throw new Error(`No controlled image at ${index}`);
      const completion = pendingEncodes[baseEncodeIndex + index];
      image.onerror?.();
      return Promise.resolve(completion)
        .catch(() => {})
        .then(async () => {
          await Promise.resolve();
          await Promise.resolve();
        });
    },
  };
}

// A fake TextEditor backed by a plain in-memory variable - useAttachments'
// own contract (see its header comment) is that it never touches a DOM
// node directly, so its tests need no real textarea element at all, only
// something implementing read()/write().
//
// Deliberately NOT a regression vector for the per-render-closure
// staleness class Composer.tsx's own TextEditor implementation once had
// (read()/write() closing over a specific render's `text` const, so a
// callback resuming at a LATER render silently reverted everything typed
// since - see Composer.tsx's textRef/cursorToRestoreRef comments and
// Composer.test.tsx's "critical" regression test). This fake's read()
// always sees write()'s latest value, by construction (one shared closure
// over `text`/`cursor`, not one per render) - useAttachments itself never
// creates multiple TextEditor instances or holds one across renders, so
// there is nothing IN THIS MODULE that staleness could come from; it is
// entirely a property of how a REAL caller wires read()/write() to its own
// per-render state. Composer.test.tsx is what has to (and does) catch that
// class - this file's job is only to prove useAttachments' own
// orchestration (limits, markers, encode, marker bookkeeping) is correct
// against a TextEditor that behaves exactly to contract.
interface FakeEditor extends TextEditor {
  getText(): string;
  getCursor(): number;
}

function makeFakeEditor(
  initialText = "",
  initialCursor = initialText.length,
  initialSelection: { start: number; end: number } = { start: initialCursor, end: initialCursor },
): FakeEditor {
  let text = initialText;
  let cursor = initialCursor;
  let selection: { start: number; end: number } = initialSelection;
  return {
    read: () => ({ text, cursor, selection }),
    write: (nextText, nextCursor) => {
      text = nextText;
      cursor = nextCursor;
      selection = { start: nextCursor, end: nextCursor };
    },
    getText: () => text,
    getCursor: () => cursor,
  };
}

function makeFile(name: string, type = "image/png", size = 1024): File {
  const bytes = new Uint8Array(size);
  return new File([bytes], name, { type });
}

async function flush(): Promise<void> {
  await act(async () => {
    for (;;) {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      const settledDecodes = decodes.splice(0);
      const settledEncodes = pendingEncodes.splice(0);
      if (settledDecodes.length === 0 && settledEncodes.length === 0) break;
      await Promise.allSettled([...settledDecodes, ...settledEncodes]);
    }
  });
}

test("an accepted attachment replaces a non-collapsed selection with its marker", async () => {
  const editor = makeFakeEditor("keep SELECTED tail", 13, { start: 5, end: 13 });
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("shot.png")], () => {});
  });

  // The selection is replaced, not pushed aside: everything outside it survives
  // and the cursor lands after the marker.
  expect(editor.getText()).toBe("keep [image 1] tail");
  expect(editor.getCursor()).toBe("keep [image 1]".length);

  // ingestFiles also starts the encode; drain it so the test leaves nothing
  // settling after it returns, as its siblings in this file do.
  await flush();
  expect(result.current.items[0]).toMatchObject({ pending: false });
});

test("replaceWithSettled hydrates recovery attachments without re-encoding", () => {
  const editor = makeFakeEditor();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.replaceWithSettled([
      {
        marker: 4,
        name: "proof.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
      },
    ]);
  });

  expect(result.current.items).toEqual([
    {
      marker: 4,
      name: "proof.png",
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    },
  ]);
  expect(result.current.toInputAttachments()).toEqual([
    { marker: 4, name: "proof.png", mediaType: "image/png", data: "AQID" },
  ]);
});

test("a new attachment after recovery hydration uses the next marker", async () => {
  const editor = makeFakeEditor("[image 4]");
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.replaceWithSettled([
      {
        marker: 4,
        name: "old.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
      },
    ]);
    result.current.ingestFiles([makeFile("new.png")], () => {});
  });
  await flush();

  expect(editor.getText()).toBe("[image 4][image 5]");
  expect(result.current.items.map((item) => item.marker)).toEqual([4, 5]);
});

test("reset clears settled items and restarts markers at one", async () => {
  const editor = makeFakeEditor();
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("old.png")], () => {}));
  await flush();
  expect(result.current.items[0]).toMatchObject({ marker: 1, pending: false });

  act(() => {
    result.current.reset();
    editor.write("", 0);
    result.current.ingestFiles([makeFile("new.png")], () => {});
  });

  expect(result.current.items).toHaveLength(1);
  expect(result.current.items[0]).toMatchObject({ marker: 1, name: "new.png" });
  await flush();
});

test("reset invalidates a pending encode success", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor();
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("old.png")], () => {}));
  expect(result.current.hasPending).toBe(true);

  act(() => {
    result.current.reset();
    editor.write("/goal replacement", "/goal replacement".length);
    gate.resolve();
  });
  await flush();

  expect(result.current.items).toEqual([]);
  expect(editor.getText()).toBe("/goal replacement");
});

test("reset invalidates a pending encode failure without stripping replacement text or reporting rejection", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor();
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("old.png")], onRejected));

  act(() => {
    result.current.reset();
    editor.write("/goal replacement", "/goal replacement".length);
    gate.reject();
  });
  await flush();

  expect(result.current.items).toEqual([]);
  expect(editor.getText()).toBe("/goal replacement");
  expect(onRejected).not.toHaveBeenCalled();
});

test("a rejected pending encode strips its remapped marker, not a literal marker", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor();
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("pending.png")], onRejected));
  const pending = result.current.items[0];
  if (!pending) throw new Error("missing pending image fixture");
  editor.write("[image 2]\n\n[image 1]", 20);
  act(() =>
    result.current.replaceWithSettled([
      { ...pending, marker: 2 },
      { marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  await act(async () => {
    gate.reject();
    await flush();
  });

  expect(editor.getText()).toBe("\n\n[image 1]");
  expect(result.current.items).toEqual([
    { marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
  expect(onRejected).toHaveBeenCalledTimes(1);
  expect(result.current.hasPending).toBe(false);
});

test("removing a remapped pending encode prevents its later completion from resurrecting it", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor();
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("pending.png")], onRejected));
  const pending = result.current.items[0];
  if (!pending) throw new Error("missing pending image fixture");
  editor.write("[image 2]\n\n[image 1]", 20);
  act(() =>
    result.current.replaceWithSettled([
      { ...pending, marker: 2 },
      { marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  act(() => result.current.removeItem(2));
  await act(async () => {
    gate.resolve();
    await flush();
  });

  expect(editor.getText()).toBe("\n\n[image 1]");
  expect(result.current.items).toEqual([
    { marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
  expect(onRejected).not.toHaveBeenCalled();
  expect(result.current.hasPending).toBe(false);
});

test("ingesting an image synchronously splices its marker into the editor text and flags the item pending", async () => {
  const editor = makeFakeEditor("hello", 5);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });

  expect(editor.getText()).toBe("hello[image 1]");
  expect(editor.getCursor()).toBe("hello[image 1]".length);
  expect(result.current.items).toHaveLength(1);
  expect(result.current.items[0]).toMatchObject({ marker: 1, pending: true, name: "a.png" });
  expect(result.current.hasPending).toBe(true);
  expect(result.current.refusalReason()).toBeDefined();
  await flush();
});

test("after the async re-encode resolves, the item flips to settled with data/width/height", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();

  expect(result.current.items[0]).toMatchObject({ marker: 1, pending: false, width: 8, height: 4 });
  expect(typeof result.current.items[0]?.data).toBe("string");
  expect(result.current.hasPending).toBe(false);
  expect(result.current.refusalReason()).toBeUndefined();
});

test("two files in the same ingest call get sequential markers spliced side by side", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png"), makeFile("b.png")], () => {});
  });

  expect(editor.getText()).toBe("[image 1][image 2]");
  expect(result.current.items.map((i) => i.marker)).toEqual([1, 2]);
  await flush();
  expect(result.current.items.every((i) => !i.pending)).toBe(true);
});

test("a non-image file is rejected: no item, no marker, and onRejected receives the reason", () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles([makeFile("notes.txt", "text/plain")], onRejected);
  });

  expect(editor.getText()).toBe("");
  expect(result.current.items).toHaveLength(0);
  expect(onRejected).toHaveBeenCalledTimes(1);
  expect(onRejected.mock.calls[0]?.[0]).toContain("notes.txt");
});

test("an oversized image is rejected before decode, naming the file", () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles([makeFile("big.png", "image/png", MAX_SOURCE_BYTES + 1)], onRejected);
  });

  expect(result.current.items).toHaveLength(0);
  expect(onRejected).toHaveBeenCalledTimes(1);
  expect(onRejected.mock.calls[0]?.[0]).toContain("big.png");
  expect(encodeSpy).not.toHaveBeenCalled();
});

test("a mixed accept+reject batch combines all rejections into one onRejected call while still accepting the good file", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles(
      [makeFile("ok.png"), makeFile("a.txt", "text/plain"), makeFile("b.pdf", "application/pdf")],
      onRejected,
    );
  });

  expect(result.current.items).toHaveLength(1);
  expect(onRejected).toHaveBeenCalledTimes(1);
  const message = onRejected.mock.calls[0]?.[0] as string;
  expect(message).toContain("a.txt");
  expect(message).toContain("b.pdf");
  await flush();
});

test("the 9th image in one session is rejected on the count cap, naming the file", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles(
      Array.from({ length: 8 }, (_, i) => makeFile(`${i}.png`)),
      () => {},
    );
  });
  expect(result.current.items).toHaveLength(8);

  const onRejected = vi.fn();
  act(() => {
    result.current.ingestFiles([makeFile("ninth.png")], onRejected);
  });
  expect(result.current.items).toHaveLength(8);
  expect(onRejected).toHaveBeenCalledTimes(1);
  expect(onRejected.mock.calls[0]?.[0]).toContain("ninth.png");
  await flush();
});

test("a decode failure strips the marker from the editor text, drops the item, and reports via onRejected", async () => {
  const editor = makeFakeEditor("", 0);
  installDecodeErrorStub();
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles([makeFile("bad.png")], onRejected);
  });
  expect(editor.getText()).toBe("[image 1]");
  await flush();

  expect(editor.getText()).toBe("");
  expect(result.current.items).toHaveLength(0);
  expect(onRejected).toHaveBeenCalledTimes(1);
});

// kata kt4j: removing a pending attachment must not toast its later decode
// failure - the user already discarded it, so a decode that finishes
// afterward (the browser has no way to cancel an in-flight Image/canvas
// decode) must be silently swallowed instead of reported.
test("removing a pending attachment before its decode fails suppresses the later rejection entirely", async () => {
  const editor = makeFakeEditor("", 0);
  installDecodeErrorStub();
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles([makeFile("bad.png")], onRejected);
  });
  expect(editor.getText()).toBe("[image 1]");

  act(() => {
    result.current.removeItem(1);
  });
  expect(editor.getText()).toBe("");
  expect(result.current.items).toHaveLength(0);

  await flush(); // let the decode's rejection actually settle

  expect(onRejected).not.toHaveBeenCalled();
  expect(editor.getText()).toBe(""); // the late rejection must not re-touch already-clean text
});

test("removeItem strips the marker text and removes exactly that item", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png"), makeFile("b.png")], () => {});
  });
  await flush();

  act(() => {
    result.current.removeItem(1);
  });

  expect(editor.getText()).toBe("[image 2]");
  expect(result.current.items.map((i) => i.marker)).toEqual([2]);
});

test("marker numbering never reuses a number removed via removeItem", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();
  act(() => {
    result.current.removeItem(1);
  });
  expect(result.current.items).toHaveLength(0);

  act(() => {
    result.current.ingestFiles([makeFile("b.png")], () => {});
  });
  expect(result.current.items[0]?.marker).toBe(2);
  await flush();
});

test("clearSubmitted removes exactly the submitted markers, leaving items added mid-flight intact", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();
  const submittedMarkers = new Set(result.current.items.map((i) => i.marker));

  // Simulate a new attachment arriving while that submit is still in flight.
  act(() => {
    result.current.ingestFiles([makeFile("b.png")], () => {});
  });
  await flush();

  act(() => {
    result.current.clearSubmitted(submittedMarkers);
  });

  expect(result.current.items).toHaveLength(1);
  expect(result.current.items[0]?.name).toBe("b.png");
});

// clearSubmitted previously only ever filtered `items` state, never the
// editor's own text - harmless when the whole textarea gets blanked
// alongside it (Composer.tsx's own clearIfUnchanged, the common case), but
// a genuinely stale, un-removable "[image N]" marker was left orphaned in
// the textarea/draft whenever the text had ALREADY diverged from the
// submitted snapshot (a concurrent edit mid-flight) - the marker's own
// backing item vanishes from `items` (no tile, no bytes) while its literal
// text lingers with nothing left to represent it.
test("clearSubmitted strips the submitted markers' own text from the editor too - a concurrent edit must not leave an orphaned marker behind", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();
  const submittedMarkers = new Set(result.current.items.map((i) => i.marker));

  // A concurrent composer edit lands (via the SAME editor, bypassing this
  // hook) while the submission carrying marker 1 is still in flight -
  // mirrors Composer.tsx's own textEditor.write() being called from a
  // fireEvent.change handler mid-submit (Composer.test.tsx's own "text
  // typed while a send is still in flight" idiom).
  act(() => {
    const newText = `${editor.getText()} plus more`;
    editor.write(newText, newText.length);
  });

  act(() => {
    result.current.clearSubmitted(submittedMarkers);
  });

  expect(editor.getText()).toBe(" plus more"); // marker 1's text is gone; the concurrent edit survives
  expect(result.current.items).toHaveLength(0);
});

test("clearSubmitted resets the marker counter to restart at 1 once the result is empty", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();
  const submittedMarkers = new Set(result.current.items.map((i) => i.marker));

  act(() => {
    result.current.clearSubmitted(submittedMarkers);
  });
  expect(result.current.items).toHaveLength(0);

  act(() => {
    result.current.ingestFiles([makeFile("fresh.png")], () => {});
  });
  expect(result.current.items[0]?.marker).toBe(1);
  await flush();
});

test("clearSubmitted does NOT reset the counter when surviving (mid-flight) items remain", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();
  const submittedMarkers = new Set(result.current.items.map((i) => i.marker));

  act(() => {
    result.current.ingestFiles([makeFile("b.png")], () => {});
  });
  await flush();

  act(() => {
    result.current.clearSubmitted(submittedMarkers);
  });
  expect(result.current.items).toHaveLength(1);

  act(() => {
    result.current.ingestFiles([makeFile("c.png")], () => {});
  });
  // b.png kept marker 2; c.png must be 3, never reusing 1.
  expect(result.current.items.map((i) => i.marker)).toEqual([2, 3]);
  await flush();
});

test("toInputAttachments maps settled items to the {marker, mediaType, data, name} shape only", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png")], () => {});
  });
  await flush();

  const attachments = result.current.toInputAttachments();
  expect(attachments).toHaveLength(1);
  expect(attachments[0]).toEqual({
    marker: 1,
    mediaType: "image/png",
    data: result.current.items[0]?.data,
    name: "a.png",
  });
});

test("hasPending stays true until every in-flight item has settled", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("a.png"), makeFile("b.png")], () => {});
  });
  expect(result.current.hasPending).toBe(true);
  await flush();
  expect(result.current.hasPending).toBe(false);
});

// RoboRev PR1131 finding 1: removeItem records EVERY removed marker in
// removedWhilePendingRef, including already-settled ones, and clearSubmitted
// resets nextMarkerRef to zero (restarting numbering at 1) without clearing
// that set. The next attachment then reuses marker 1 and its successful encode
// is discarded as if the user had removed it, leaving the item pending forever.
test("an attachment that reuses a retired marker after clearSubmitted still settles", async () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));

  act(() => result.current.ingestFiles([makeFile("old.png")], () => {}));
  await flush();
  expect(result.current.items[0]).toMatchObject({ marker: 1, pending: false });

  act(() => result.current.removeItem(1));
  expect(result.current.items).toHaveLength(0);

  // Numbering restarts at 1 once the result is empty (clearSubmitted), so the
  // fresh attachment takes marker 1 again - the retired marker must not poison it.
  act(() => result.current.clearSubmitted(new Set()));
  act(() => result.current.ingestFiles([makeFile("fresh.png")], () => {}));
  expect(result.current.items[0]?.marker).toBe(1);

  await flush();
  expect(result.current.items[0]).toMatchObject({ marker: 1, name: "fresh.png", pending: false });
});

// The rebind repair: a retained tile's number can name an occurrence the
// incoming draft spells as ordinary prose. The tile must move off it, and the
// text must not change at all - this text belongs to the writer that produced
// it, not to this composer's older tiles.
test("renumberOff moves a retained tile off a marker the text spells, leaving the text byte-identical", async () => {
  const editor = makeFakeEditor("");
  const { result } = renderHook(() => useAttachments(editor));
  act(() =>
    result.current.replaceWithSettled([
      { marker: 1, name: "retained.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  act(() => result.current.renumberOff("incoming prose [image 1] tail"));

  expect(result.current.items).toEqual([
    { marker: 2, name: "retained.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
  // Byte-identical: the text was never written through the editor at all.
  expect(editor.getText()).toBe("");
  expect(result.current.toInputAttachments()).toEqual([
    { marker: 2, name: "retained.png", mediaType: "image/png", data: "AQID" },
  ]);
});

test("renumberOff leaves a tile whose marker the text does not spell", () => {
  const editor = makeFakeEditor("");
  const { result } = renderHook(() => useAttachments(editor));
  act(() =>
    result.current.replaceWithSettled([
      { marker: 1, name: "retained.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  act(() => result.current.renumberOff("incoming prose [image 2] tail"));

  expect(result.current.items.map((item) => item.marker)).toEqual([1]);
});

test("renumberOff skips numbers the caller and the store reserve", () => {
  const editor = makeFakeEditor("");
  const { result } = renderHook(() => useAttachments(editor));
  act(() =>
    result.current.replaceWithSettled([
      { marker: 1, name: "a.png", mediaType: "image/png", data: "AQID", pending: false },
      { marker: 2, name: "b.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  // 1 is spelled; 3 is reserved by the caller. The free number is 4.
  act(() => result.current.renumberOff("[image 1]", [3]));

  expect(result.current.items.map((item) => item.marker)).toEqual([4, 2]);
});

// Every moved tile takes a distinct number: two tiles spelled in one text must
// never collapse onto a single marker, which would make them one identity.
test("renumberOff gives each moved tile its own number", () => {
  const editor = makeFakeEditor("");
  const { result } = renderHook(() => useAttachments(editor));
  act(() =>
    result.current.replaceWithSettled([
      { marker: 1, name: "a.png", mediaType: "image/png", data: "AQID", pending: false },
      { marker: 2, name: "b.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  act(() => result.current.renumberOff("[image 1] and [image 2]"));

  const markers = result.current.items.map((item) => item.marker);
  expect(new Set(markers).size).toBe(2);
  expect(markers.every((marker) => marker > 2)).toBe(true);
});

// A moved tile keeps its identity through the move: the encode continuation
// that is still in flight must settle the MOVED tile, not leave it pending.
test("renumberOff preserves an in-flight encode's identity across the move", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor("");
  const { result } = renderHook(() => useAttachments(editor));
  act(() => result.current.ingestFiles([makeFile("pending.png")], () => {}));
  expect(result.current.items[0]).toMatchObject({ marker: 1, pending: true });

  act(() => result.current.renumberOff("incoming [image 1]"));

  await act(async () => {
    gate.resolve();
    await flush();
  });

  expect(result.current.items).toEqual([
    { marker: 2, name: "pending.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 },
  ]);
  expect(result.current.hasPending).toBe(false);
});

test("renumberOff leaves the numbering high-water mark above every number it assigned", async () => {
  const editor = makeFakeEditor("[image 1]");
  const { result } = renderHook(() => useAttachments(editor));
  act(() =>
    result.current.replaceWithSettled([
      { marker: 1, name: "retained.png", mediaType: "image/png", data: "AQID", pending: false },
    ]),
  );

  act(() => result.current.renumberOff("[image 1]"));
  act(() => result.current.ingestFiles([makeFile("fresh.png")], () => {}));
  await flush();

  // 1 is spelled by the text AND held by the moved tile at 2, so the fresh
  // gesture starts at 3 rather than reusing either.
  expect(result.current.items.map((item) => item.marker)).toEqual([2, 3]);
});

test("a 10 MB JPEG passes pre-decode admission and settles with image/jpeg mediaType and resized dimensions", async () => {
  installImageDimensionsStub(4032, 3024);
  const editor = makeFakeEditor("", 0);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("photo.jpg", "image/jpeg", 10 * 1024 * 1024)], onRejected);
  });

  expect(onRejected).not.toHaveBeenCalled();
  expect(editor.getText()).toBe("[image 1]");
  expect(encodeSpy).toHaveBeenCalledTimes(1);

  await flush();

  expect(onRejected).not.toHaveBeenCalled();
  expect(result.current.hasPending).toBe(false);
  expect(result.current.items).toEqual([
    {
      marker: 1,
      name: "photo.jpg",
      mediaType: "image/jpeg",
      width: 2048,
      height: 1536,
      data: "AQID",
      pending: false,
    },
  ]);
  expect(result.current.toInputAttachments()).toEqual([
    {
      marker: 1,
      name: "photo.jpg",
      mediaType: "image/jpeg",
      data: "AQID",
    },
  ]);
});

test("a 26 MB JPEG fails pre-decode admission with zero staged markers", () => {
  const editor = makeFakeEditor("keep ", 5);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("huge.jpg", "image/jpeg", 26 * 1024 * 1024)], onRejected);
  });

  expect(editor.getText()).toBe("keep ");
  expect(result.current.items).toEqual([]);
  expect(onRejected).toHaveBeenCalledTimes(1);
  expect(onRejected.mock.calls[0]?.[0]).toContain("huge.jpg");
  expect(encodeSpy).not.toHaveBeenCalled();
});

test("post-decode ImageEncodeError maps tooManyPixels, tooLarge, and decodeFailed, stripping tile and marker", async () => {
  const reasons: string[] = [];

  for (const mode of ["tooManyPixels", "tooLarge", "decodeFailed"] as const) {
    const defaultToBlob = HTMLCanvasElement.prototype.toBlob;
    try {
      if (mode === "tooManyPixels") {
        expect(8064 * 6049).toBeGreaterThan(MAX_SOURCE_PIXELS);
        installImageDimensionsStub(8064, 6049);
      } else if (mode === "tooLarge") {
        installImageDimensionsStub(800, 600);
        HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
          callback(new Blob([new Uint8Array(MAX_ATTACHMENT_BYTES + 1)], { type: "image/jpeg" }));
        };
      } else {
        installDecodeErrorStub();
      }

      const editor = makeFakeEditor("keep ", 5);
      const onRejected = vi.fn();
      const { result } = renderHook(() => useAttachments(editor));

      act(() => {
        result.current.ingestFiles([makeFile("bad.jpg", "image/jpeg", 1024)], onRejected);
      });
      expect(editor.getText()).toBe("keep [image 1]");
      expect(result.current.items).toHaveLength(1);

      await flush();

      expect(editor.getText()).toBe("keep ");
      expect(result.current.items).toEqual([]);
      expect(result.current.hasPending).toBe(false);
      expect(onRejected).toHaveBeenCalledTimes(1);
      const reason = onRejected.mock.calls[0]?.[0] as string;
      expect(reason).toContain("bad.jpg");
      reasons.push(reason);
    } finally {
      HTMLCanvasElement.prototype.toBlob = defaultToBlob;
    }
  }

  expect(new Set(reasons).size).toBe(3);
});

test("a multi-file batch runs one encodeImage call at a time in FIFO order", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor("", 0);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("first.png"), makeFile("second.png"), makeFile("third.png")], onRejected);
  });

  expect(editor.getText()).toBe("[image 1][image 2][image 3]");
  expect(result.current.items.map((item) => ({ marker: item.marker, pending: item.pending }))).toEqual([
    { marker: 1, pending: true },
    { marker: 2, pending: true },
    { marker: 3, pending: true },
  ]);
  expect(encodeSpy).toHaveBeenCalledTimes(1);
  expect(gate.count()).toBe(1);

  await act(async () => {
    await gate.resolve(0);
  });
  expect(result.current.items.map((item) => item.pending)).toEqual([false, true, true]);
  expect(encodeSpy).toHaveBeenCalledTimes(2);
  expect(gate.count()).toBe(2);

  await act(async () => {
    await gate.resolve(1);
  });
  expect(result.current.items.map((item) => item.pending)).toEqual([false, false, true]);
  expect(encodeSpy).toHaveBeenCalledTimes(3);
  expect(gate.count()).toBe(3);

  await act(async () => {
    await gate.resolve(2);
  });
  expect(result.current.items.map((item) => item.pending)).toEqual([false, false, false]);
  expect(result.current.hasPending).toBe(false);
  expect(onRejected).not.toHaveBeenCalled();
});

test("removing a queued attachment before its serial turn starts skips encodeImage and suppresses onRejected", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor("", 0);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("first.png"), makeFile("queued.png"), makeFile("third.png")], onRejected);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(1);

  act(() => {
    result.current.removeItem(2);
  });
  expect(editor.getText()).toBe("[image 1][image 3]");
  expect(result.current.items.map((item) => item.marker)).toEqual([1, 3]);

  await act(async () => {
    await gate.resolve(0);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(2);
  expect(gate.count()).toBe(2);

  await act(async () => {
    await gate.resolve(1);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(2);
  expect(result.current.items.map((item) => ({ marker: item.marker, name: item.name, pending: item.pending }))).toEqual(
    [
      { marker: 1, name: "first.png", pending: false },
      { marker: 3, name: "third.png", pending: false },
    ],
  );
  expect(editor.getText()).toBe("[image 1][image 3]");
  expect(onRejected).not.toHaveBeenCalled();
  expect(result.current.hasPending).toBe(false);
});

test("resetting before queued attachments start skips encodeImage, preserves replacement text, and suppresses onRejected", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor("", 0);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("first.png"), makeFile("queued.png")], onRejected);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(1);

  act(() => {
    result.current.reset();
    editor.write("/goal replacement", "/goal replacement".length);
  });

  await act(async () => {
    await gate.reject(0);
  });

  expect(encodeSpy).toHaveBeenCalledTimes(1);
  expect(result.current.items).toEqual([]);
  expect(editor.getText()).toBe("/goal replacement");
  expect(onRejected).not.toHaveBeenCalled();
  expect(result.current.hasPending).toBe(false);
});

test("resetting while an encode is in flight still serializes a subsequent ingest behind it", async () => {
  const gate = installControlledImageStub();
  const editor = makeFakeEditor("", 0);
  const onRejected = vi.fn();
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.ingestFiles([makeFile("stale.png"), makeFile("queued-stale.png")], onRejected);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(1);

  act(() => {
    result.current.reset();
    editor.write("/goal ", "/goal ".length);
    result.current.ingestFiles([makeFile("fresh.png")], onRejected);
  });
  expect(editor.getText()).toBe("/goal [image 1]");
  expect(result.current.items.map((item) => ({ marker: item.marker, name: item.name, pending: item.pending }))).toEqual(
    [{ marker: 1, name: "fresh.png", pending: true }],
  );
  // stale.png is still decoding in the browser, so fresh.png waits until it settles.
  expect(encodeSpy).toHaveBeenCalledTimes(1);

  await act(async () => {
    await gate.resolve(0);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(2);
  expect(gate.count()).toBe(2);

  await act(async () => {
    await gate.resolve(1);
  });
  expect(encodeSpy).toHaveBeenCalledTimes(2);
  expect(result.current.items).toEqual([
    { marker: 1, name: "fresh.png", mediaType: "image/png", width: 8, height: 4, data: "AQID", pending: false },
  ]);
  expect(editor.getText()).toBe("/goal [image 1]");
  expect(onRejected).not.toHaveBeenCalled();
});
