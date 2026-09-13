import { describe, expect, it } from "vitest";
import {
	editPairingInput,
	importPairing,
	reviewPairingInput,
} from "./pairingImport";

describe("pairing import review", () => {
	it("previews the origin and keeps the token transient", () => {
		expect(
			reviewPairingInput("https://hub.example/auth/secret%23token"),
		).toEqual({
			input: "https://hub.example/auth/secret%23token",
			preview: { origin: "https://hub.example", token: "secret#token" },
			error: null,
		});
	});
	it("editing clears a stale preview and import is the only transition that yields fields", () => {
		const reviewed = reviewPairingInput("https://hub.example/auth/token");
		expect(editPairingInput("https://hub.example/auth/changed")).toEqual({
			input: "https://hub.example/auth/changed",
			preview: null,
			error: null,
		});
		expect(importPairing(reviewed)).toEqual({
			origin: "https://hub.example",
			token: "token",
			state: { input: "", preview: null, error: null },
		});
	});
	it("returns a generic error and no preview for invalid links", () => {
		expect(reviewPairingInput("https://hub.example/auth/%ZZ")).toEqual({
			input: "https://hub.example/auth/%ZZ",
			preview: null,
			error: "Invalid pairing URL.",
		});
		expect(importPairing(reviewPairingInput("bad"))).toBeNull();
	});
});
