import { describe, expect, it } from "vitest";
import type { ConversationMutationState } from "../../mobile/src/state/conversation";
import {
  captureUnconfirmedSend,
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
  it("preserves submitted text separately from an empty composer", () => {
    expect(captureUnconfirmedSend(pending)).toBe(submitted);
  });

  it("does not recover a completed, failed, or interrupt mutation as an unconfirmed send", () => {
    expect(captureUnconfirmedSend(null)).toBeNull();
    expect(captureUnconfirmedSend({ ...pending, status: "failed" })).toBeNull();
    expect(
      captureUnconfirmedSend({
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
