import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { translateAttachmentMarkers } from "../../../stores/attachmentMarkers";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { TurnBlock } from "./TurnBlock";
import { originatingInput, TurnFailureEndCap } from "./TurnFailureEndCap";

beforeEach(() => {
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});
afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_u", turnId: "turn_1", type: "userMessage", text: "do the thing", ...overrides };
}

function failedTurn(overrides: Partial<TurnModel> = {}): TurnModel {
  return {
    id: "turn_1",
    status: "failed",
    items: [item()],
    error: { message: "the provider exploded" },
    ...overrides,
  };
}

test("renders the taxonomy badge and the error message", () => {
  render(<TurnFailureEndCap error={{ message: "the provider exploded" }} turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy(); // no source/cause -> generic badge
  expect(screen.getByText("the provider exploded")).toBeTruthy();
});

test("a provider failure shows a provider-status badge and a Retry action", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "429 rate limited", cause: { kind: "provider", status: 429 } }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("provider 429")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("a connection failure offers a Reconnect & retry action", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "local daemon unavailable", source: "hub" }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("connection")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reconnect & retry" })).toBeTruthy();
});

test("the hint renders when present", () => {
  render(
    <TurnFailureEndCap
      error={{ message: "boom", hint: "check your API key" }}
      turn={failedTurn()}
      sessionRef="ref_a"
    />,
  );
  expect(screen.getByText("What can I do?")).toBeTruthy();
  expect(screen.queryByText("check your API key")).toBeNull();
});

test("hint sits behind a disclosure; retry is inline in the head row", () => {
  render(
    <TurnFailureEndCap error={{ message: "boom", hint: "check your API key" }} turn={failedTurn()} sessionRef="s1" />,
  );
  const cap = screen.getByTestId("turn-failure");
  const head = cap.firstElementChild as HTMLElement;
  expect(head.contains(screen.getByRole("button", { name: /retry/i }))).toBe(true);
  expect(screen.queryByText("check your API key")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "What can I do?" }));
  expect(screen.getByText("check your API key")).toBeTruthy();
});

test("clicking retry re-issues the turn's user input via threadsStore.send", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn()} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "do the thing", undefined));
});

test("clicking retry re-sends the originating input's image attachments", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const turn = failedTurn({
    items: [item({ images: [{ src: "data:image/png;base64,aGVsbG8=", name: "pic.png" }] })],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "do the thing", [
      { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
    ]),
  );
});

test("retry pairs each image with its translated marker number", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const turn = failedTurn({
    items: [
      item({
        text: "(attached image 7: seven.png) then (attached image 3)",
        images: [
          { src: "data:image/png;base64,c2V2ZW4=", name: "seven.png" },
          { src: "data:image/jpeg;base64,dGhyZWU=" },
        ],
      }),
    ],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "[image 7] then [image 3]", [
      { marker: 7, mediaType: "image/png", data: "c2V2ZW4=", name: "seven.png" },
      { marker: 3, mediaType: "image/jpeg", data: "dGhyZWU=" },
    ]),
  );
  const sentCall = sendSpy.mock.calls[0];
  if (!sentCall) throw new Error("send was not called");
  const [, sentText, sentAttachments] = sentCall;
  expect(translateAttachmentMarkers(sentText, sentAttachments)).toBe(
    "(attached image 7: seven.png) then (attached image 3)",
  );
});

test("retry warns instead of silently resending text when image bytes are unavailable", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const turn = failedTurn({
    items: [item({ text: "look at this", images: [{ src: "/s/sess_1/images/abc", name: "shot.png" }] })],
  });
  render(
    <>
      <TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />
      <Toast />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "look at this", undefined));
  expect(await screen.findByText(/Retried without an attached image/)).toBeTruthy();
  expect(getToasts().map((toast) => toast.kind)).toEqual(["warning"]);
});

test("without a session ref the diagnostic still renders but the recovery action is withheld", () => {
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn()} sessionRef={undefined} />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
  expect(screen.getByText("boom")).toBeTruthy();
  expect(screen.queryByRole("button")).toBe(null);
});

test("a failed turn with no user-input item to retry withholds the action even with a ref", () => {
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={failedTurn({ items: [] })} sessionRef="ref_a" />);
  expect(screen.queryByRole("button")).toBe(null);
});

// --- a reloaded failure offers the same recovery a live one does (kata 0wb6)
// One persisted transcript entry is one turn, so a RELOADED failure is a turn
// of its own carrying only the failure item - the input that opened the
// exchange sits in an earlier turn. Retry was therefore offered live and
// withheld after reload, for the same failure.

function seedThread(ref: string, turns: TurnModel[]): void {
  threadsStore.setState({ threads: new Map([[ref, { ref, turns } as unknown as ThreadModel]]) });
}

const RELOADED_FAILURE: TurnModel = {
  id: "turn_2",
  status: "failed",
  items: [{ id: "item_turn_failure_2", turnId: "turn_2", type: "systemMessage", text: "boom", eventKind: "error" }],
  error: { message: "boom" },
};

function reloadedThread(): TurnModel[] {
  return [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "explain parser.go" })] },
    RELOADED_FAILURE,
  ];
}

test("a reloaded failure offers Retry, sourced from the input that opened the exchange", () => {
  seedThread("ref_a", reloadedThread());
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("retrying a reloaded failure re-issues that earlier input", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  seedThread("ref_a", reloadedThread());
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "explain parser.go", undefined));
});

test("the lookback stops at the failed turn, never re-issuing an input sent after it", () => {
  seedThread("ref_a", [
    ...reloadedThread(),
    { id: "turn_3", status: "completed", items: [item({ turnId: "turn_3", text: "a later, unrelated prompt" })] },
  ]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("a later, unrelated prompt")).toBe(null);
  expect(originatingInput(threadsStore.getState().threads.get("ref_a")?.turns ?? [], "turn_2")).toEqual({
    kind: "retry",
    input: { text: "explain parser.go", sourceImageCount: 0 },
  });
});

test("a thread whose turns hold no user input at all still withholds the action", () => {
  seedThread("ref_a", [RELOADED_FAILURE]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.queryByRole("button")).toBe(null);
});

test("retrying a reloaded input with unresolvable bytes warns from the originating item", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  seedThread("ref_a", [
    {
      id: "turn_1",
      status: "completed",
      items: [
        item({
          turnId: "turn_1",
          text: "look at this",
          images: [{ src: "/s/sess_1/images/abc", name: "shot.png" }],
        }),
      ],
    },
    RELOADED_FAILURE,
  ]);
  render(
    <>
      <TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />
      <Toast />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", "look at this", undefined));
  expect(await screen.findByText(/Retried without an attached image/)).toBeTruthy();
  expect(getToasts().map((toast) => toast.kind)).toEqual(["warning"]);
});

test("an image-only input is retryable even with empty text", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const turn = failedTurn({
    items: [item({ text: "   ", images: [{ src: "data:image/png;base64,aGVsbG8=", name: "pic.png" }] })],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "", [
      { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
    ]),
  );
});

test("duplicate attachment names refuse the images instead of guessing", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const text = "(attached image 1: dup.png) and (attached image 2: dup.png)";
  const turn = failedTurn({
    items: [
      item({
        text,
        images: [
          { src: "data:image/png;base64,Ynl0ZXMtMQ==", name: "dup.png" },
          { src: "data:image/png;base64,Ynl0ZXMtMg==", name: "dup.png" },
        ],
      }),
    ],
  });
  render(
    <>
      <TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />
      <Toast />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(sendSpy).toHaveBeenCalledWith("ref_a", text, undefined));
  expect(await screen.findByText(/Retried without 2 attached images/)).toBeTruthy();
});

test("duplicate names with no prose mentions still retry - there are no markers to misassign", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  seedThread("ref_a", [
    {
      id: "turn_1",
      status: "completed",
      items: [
        item({
          turnId: "turn_1",
          text: "",
          images: [
            { src: "data:image/png;base64,Ynl0ZXMtMQ==", name: "dup.png" },
            { src: "data:image/png;base64,Ynl0ZXMtMg==", name: "dup.png" },
          ],
        }),
      ],
    },
    RELOADED_FAILURE,
  ]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "", [
      { marker: 1, mediaType: "image/png", data: "Ynl0ZXMtMQ==", name: "dup.png" },
      { marker: 2, mediaType: "image/png", data: "Ynl0ZXMtMg==", name: "dup.png" },
    ]),
  );
});

test("a filename containing a paren pairs by full name and round-trips", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const text = "(attached image 1: plot).png) describe it";
  const turn = failedTurn({
    items: [item({ text, images: [{ src: "data:image/png;base64,cGxvdA==", name: "plot).png" }] })],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "[image 1] describe it", [
      { marker: 1, mediaType: "image/png", data: "cGxvdA==", name: "plot).png" },
    ]),
  );
  const sentCall = sendSpy.mock.calls[0];
  if (!sentCall) throw new Error("send was not called");
  const [, sentText, sentAttachments] = sentCall;
  expect(translateAttachmentMarkers(sentText, sentAttachments)).toBe(text);
});

test("prose naming an image that was never attached stays verbatim", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const text = "(attached image 9: ghost) hi";
  const turn = failedTurn({
    items: [item({ text, images: [{ src: "data:image/png;base64,cmVhbA==", name: "real.png" }] })],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", text, [
      { marker: 1, mediaType: "image/png", data: "cmVhbA==", name: "real.png" },
    ]),
  );
});

test("an image-only input with unavailable bytes stops the lookback instead of retrying an older prompt", () => {
  const turns: TurnModel[] = [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "an older, unrelated prompt" })] },
    {
      id: "turn_2",
      status: "completed",
      items: [
        item({
          turnId: "turn_2",
          id: "item_img",
          text: "",
          images: [{ src: "/s/sess_1/images/abc", name: "shot.png" }],
        }),
      ],
    },
    RELOADED_FAILURE,
  ];
  expect(originatingInput(turns, "turn_2")).toEqual({ kind: "images-unavailable", sourceImageCount: 1 });
});

test("an image-only input with unavailable bytes shows a re-attach note instead of Retry", () => {
  seedThread("ref_a", [
    {
      id: "turn_1",
      status: "completed",
      items: [item({ turnId: "turn_1", text: "", images: [{ src: "/s/sess_1/images/abc", name: "shot.png" }] })],
    },
    RELOADED_FAILURE,
  ]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  expect(screen.queryByRole("button", { name: "Retry" })).toBe(null);
  expect(screen.getByText(/re-attach the image to retry/)).toBeTruthy();
});

test("retry sends composer-style anchors so a failed retry recovers its image tiles", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  seedThread("ref_a", [
    {
      id: "turn_1",
      status: "completed",
      items: [
        item({
          turnId: "turn_1",
          text: "(attached image 2: diagram.png)explain this",
          images: [{ src: "data:image/png;base64,ZGlhZ3JhbQ==", name: "diagram.png" }],
        }),
      ],
    },
    RELOADED_FAILURE,
  ]);
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={RELOADED_FAILURE} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "[image 2]explain this", [
      { marker: 2, mediaType: "image/png", data: "ZGlhZ3JhbQ==", name: "diagram.png" },
    ]),
  );
  const sentCall = sendSpy.mock.calls[0];
  if (!sentCall) throw new Error("send was not called");
  const [, sentText, sentAttachments] = sentCall;
  expect(translateAttachmentMarkers(sentText, sentAttachments)).toBe("(attached image 2: diagram.png)explain this");
});

test("retry pairs markers by attachment name when the prose order diverges from send order", async () => {
  const sendSpy = vi.spyOn(threadsStore.getState(), "send").mockResolvedValue(undefined);
  const turn = failedTurn({
    items: [
      item({
        text: "(attached image 2: b.png) then (attached image 1: a.png)",
        images: [
          { src: "data:image/png;base64,Ynl0ZXMtYQ==", name: "a.png" },
          { src: "data:image/png;base64,Ynl0ZXMtYg==", name: "b.png" },
        ],
      }),
    ],
  });
  render(<TurnFailureEndCap error={{ message: "boom" }} turn={turn} sessionRef="ref_a" />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() =>
    expect(sendSpy).toHaveBeenCalledWith("ref_a", "[image 2] then [image 1]", [
      { marker: 1, mediaType: "image/png", data: "Ynl0ZXMtYQ==", name: "a.png" },
      { marker: 2, mediaType: "image/png", data: "Ynl0ZXMtYg==", name: "b.png" },
    ]),
  );
  const sentCall = sendSpy.mock.calls[0];
  if (!sentCall) throw new Error("send was not called");
  const [, sentText, sentAttachments] = sentCall;
  expect(translateAttachmentMarkers(sentText, sentAttachments)).toBe(
    "(attached image 2: b.png) then (attached image 1: a.png)",
  );
});

test("originatingInput skips a whitespace-only input rather than re-issuing nothing", () => {
  const turns: TurnModel[] = [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "real work" })] },
    { id: "turn_2", status: "completed", items: [item({ turnId: "turn_2", id: "item_blank", text: "   " })] },
    RELOADED_FAILURE,
  ];
  expect(originatingInput(turns, "turn_2")).toEqual({
    kind: "retry",
    input: { text: "real work", sourceImageCount: 0 },
  });
});

test("originatingInput takes the LAST input at or before the failed turn", () => {
  const turns: TurnModel[] = [
    { id: "turn_1", status: "completed", items: [item({ turnId: "turn_1", text: "first" })] },
    { id: "turn_2", status: "completed", items: [item({ turnId: "turn_2", id: "item_u2", text: "second" })] },
    RELOADED_FAILURE,
  ];
  expect(originatingInput(turns, "turn_2")).toEqual({ kind: "retry", input: { text: "second", sourceImageCount: 0 } });
});

// --- TurnBlock integration: the end-cap is driven by turn.error presence ----

test("TurnBlock renders the failure end-cap for a failed turn (turn.error present)", () => {
  render(<TurnBlock turn={failedTurn()} sessionRef="ref_a" />);
  expect(screen.getByTestId("turn-failure")).toBeTruthy();
});

test("TurnBlock renders NO end-cap for a clean turn (no error)", () => {
  render(<TurnBlock turn={{ id: "turn_2", status: "completed", items: [item()] }} sessionRef="ref_a" />);
  expect(screen.queryByTestId("turn-failure")).toBe(null);
});
