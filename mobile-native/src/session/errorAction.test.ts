import { describe, expect, it } from "vitest";
import { errorAction } from "./errorAction";

const session = (resumeRequired = false) => ({
	resumeRequired,
	turns: [
		{ id: "turn_1" },
		{ id: "turn_2", items: [{ id: "u-1", type: "userMessage", text: "my original question" }] },
	] as never,
});
const failure = (detail: string, turnId = "turn_2", title = "The turn failed") => ({
	id: `failure:${turnId}`,
	title,
	detail,
	turnId,
});

describe("an error's one action (spec 8.2, ruling 26)", () => {
	it.each([
		["a paused session resumes", failure("go test exited 1"), session(true), true, "resume"],
		["a paused session resumes before it signs in", failure("Sign-in expired"), session(true), true, "resume"],
		["an expired sign-in signs in", failure("Sign-in expired"), session(), true, "signIn"],
		["a 401 signs in", failure("401 Unauthorized"), session(), true, "signIn"],
		["expired credentials sign in", failure("Invalid credentials expired"), session(), true, "signIn"],
		["invalid credentials sign in, either word order", failure("Invalid credentials"), session(), true, "signIn"],
		["expired credentials sign in, either word order", failure("Expired credential"), session(), true, "signIn"],
		["the latest turn's failure retries", failure("go test exited 1"), session(), true, "retry"],
		["no retry while Send can't act", failure("go test exited 1"), session(), false, null],
		["no retry under an earlier turn", failure("go test exited 1", "turn_1"), session(), true, null],
		["no retry for a warning's row", { ...failure("rate limited"), id: "warn-item-1" }, session(), true, null],
		["retry when the turn's input is gone", failure("go test exited 1"), sessionless(), true, "retry"],
	] as const)("%s", (_name, row, model, canSend, action) => {
		expect(errorAction(row, model, canSend)).toBe(action);
	});
});

/** A reloaded failure entry: no turn at or before it holds the user's input. */
function sessionless() {
	return {
		resumeRequired: false,
		turns: [
			{ id: "turn_1", items: [] },
			{ id: "turn_2", items: [] },
		] as never,
	};
}
