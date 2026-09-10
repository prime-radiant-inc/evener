import { describe, expect, it } from "vitest";
import type { ConversationMutationState } from "../../mobile/src/state/conversation";
import {
  captureUnconfirmedInput,
  restoreUnconfirmedDraft,
} from "./draftRecovery";

const submitted = "  exact draft\nwith whitespace 🦋  ";
const pending: ConversationMutationState = {
  kind: "send",
  status: "pending",
  draftSnapshot: submitted,
  draftRevisionAtSubmit: 2,
  generation: 4,
  mutationId: 7,
};

describe("draft recovery after connection loss", () => {
  it.each(["steer", "queue"] as const)(
    "retains an uncertain %s without resending it",
    (kind) => {
      expect(captureUnconfirmedInput({ ...pending, kind })).toBe(submitted);
      expect(
        captureUnconfirmedInput({ ...pending, kind, status: "failed" }),
      ).toBeNull();
    },
  );

  it("preserves submitted text separately from an empty composer", () => {
    expect(captureUnconfirmedInput(pending)).toBe(submitted);
  });

  it("does not recover a completed, failed, or interrupt mutation as an unconfirmed send", () => {
    expect(captureUnconfirmedInput(null)).toBeNull();
    expect(
      captureUnconfirmedInput({ ...pending, status: "failed" }),
    ).toBeNull();
    expect(
      captureUnconfirmedInput({
        ...pending,
        kind: "interrupt",
        draftSnapshot: null,
      }),
    ).toBeNull();
  });

  it("restores exact text only into an empty composer when explicitly requested", () => {
    expect(restoreUnconfirmedDraft("", submitted)).toBe(submitted);
  });

  it("refuses to overwrite anything subsequently typed, including whitespace", () => {
    expect(restoreUnconfirmedDraft("new draft", submitted)).toBeNull();
    expect(restoreUnconfirmedDraft(" ", submitted)).toBeNull();
  });
});
