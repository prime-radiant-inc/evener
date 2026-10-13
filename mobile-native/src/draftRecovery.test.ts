import { describe, expect, it } from "vitest";
import { restoreUnconfirmedDraft } from "./draftRecovery";

const submitted = "  exact draft\nwith whitespace 🦋  ";

describe("draft recovery after connection loss", () => {
	it("restores exact text only into an empty composer when explicitly requested", () => {
		expect(restoreUnconfirmedDraft("", submitted)).toBe(submitted);
	});

	it("refuses to overwrite anything subsequently typed, including whitespace", () => {
		expect(restoreUnconfirmedDraft("new draft", submitted)).toBeNull();
		expect(restoreUnconfirmedDraft(" ", submitted)).toBeNull();
	});
});
