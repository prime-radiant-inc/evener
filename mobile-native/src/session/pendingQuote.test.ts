import { expect, it } from "vitest";
import { holdQuote, takeQuote } from "./pendingQuote";

it("hands a held quote to its session once, and nothing to another session", () => {
	holdQuote("studio", "local:coord", "The goal is green.");
	expect(takeQuote("studio", "local:other")).toBeNull();
	expect(takeQuote("other-hub", "local:coord")).toBeNull();
	expect(takeQuote("studio", "local:coord")).toBe("The goal is green.");
	expect(takeQuote("studio", "local:coord")).toBeNull();
});

it("keeps only the newest quote for a session", () => {
	holdQuote("studio", "local:coord", "first");
	holdQuote("studio", "local:coord", "second");
	expect(takeQuote("studio", "local:coord")).toBe("second");
	expect(takeQuote("studio", "local:coord")).toBeNull();
});
