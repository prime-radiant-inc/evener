import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { type TextEditor, useAttachments } from "./useAttachments";

// Same jsdom canvas-stub technique as encodePng.test.ts (see that file's
// own header comment) - useAttachments calls the REAL reencodeToPng
// internally, so its tests exercise the real orchestration end to end
// rather than mocking the module it depends on.
let originalGetContext: typeof HTMLCanvasElement.prototype.getContext;
let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob;
let originalImage: typeof Image;
let originalCreateObjectURL: typeof URL.createObjectURL;
let originalRevokeObjectURL: typeof URL.revokeObjectURL;

beforeEach(() => {
  originalGetContext = HTMLCanvasElement.prototype.getContext;
  originalToBlob = HTMLCanvasElement.prototype.toBlob;
  originalImage = globalThis.Image;
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};

  HTMLCanvasElement.prototype.getContext = (() => ({ drawImage() {} })) as unknown as typeof originalGetContext;
  HTMLCanvasElement.prototype.toBlob = function (this: HTMLCanvasElement, callback: BlobCallback): void {
    callback(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 8;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
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

function makeFakeEditor(initialText = "", initialCursor = initialText.length): FakeEditor {
  let text = initialText;
  let cursor = initialCursor;
  return {
    read: () => ({ text, cursor }),
    write: (nextText, nextCursor) => {
      text = nextText;
      cursor = nextCursor;
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
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

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

test("ingesting an image synchronously splices its marker into the editor text and flags the item pending", () => {
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

test("an oversized image is rejected before decode, naming the 8 MB limit", () => {
  const editor = makeFakeEditor("", 0);
  const { result } = renderHook(() => useAttachments(editor));
  const onRejected = vi.fn();

  act(() => {
    result.current.ingestFiles([makeFile("big.png", "image/png", 8 * 1024 * 1024 + 1)], onRejected);
  });

  expect(result.current.items).toHaveLength(0);
  expect(onRejected.mock.calls[0]?.[0]).toContain("maximum 8 MB");
});

test("a mixed accept+reject batch combines all rejections into one onRejected call while still accepting the good file", () => {
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
});

test("the 9th image in one session is rejected on the count cap, naming the 8-image limit", () => {
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
  expect(onRejected.mock.calls[0]?.[0]).toContain("maximum 8 images");
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
