import { expect, it } from "vitest";
import { type BoardSection, onBoardJump, requestBoardJump } from "./boardJump";

it("reaches the Board, and a request made before it listens waits for it once", () => {
	requestBoardJump("needsYou");
	const heard: BoardSection[] = [];
	const stop = onBoardJump((section) => heard.push(section));
	expect(heard).toEqual(["needsYou"]);
	requestBoardJump("needsYou");
	expect(heard).toEqual(["needsYou", "needsYou"]);
	stop();
	requestBoardJump("needsYou");
	const later: BoardSection[] = [];
	onBoardJump((section) => later.push(section))();
	expect(heard).toHaveLength(2);
	expect(later).toEqual(["needsYou"]);
});
