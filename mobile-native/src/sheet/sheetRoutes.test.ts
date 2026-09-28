import { describe, expect, it } from "vitest";
import { inFront, isSheetRoute, SHEET_ROUTES, sheetOptions } from "./sheetRoutes";

describe("a sheet's size (spec 6)", () => {
	it("rests at medium and large and opens at medium, with the grabber", () => {
		expect(sheetOptions(["medium", "large"], "medium")).toEqual({
			presentation: "formSheet",
			headerShown: false,
			sheetAllowedDetents: [0.5, 1],
			sheetInitialDetentIndex: 0,
			sheetGrabberVisible: true,
		});
	});

	it("opens at large when asked, whatever order the detents were named in", () => {
		expect(sheetOptions(["large", "medium"], "large")).toMatchObject({
			sheetAllowedDetents: [0.5, 1],
			sheetInitialDetentIndex: 1,
		});
	});

	it("shows no grabber when it rests at one size", () => {
		expect(sheetOptions(["large"], "large")).toMatchObject({
			sheetAllowedDetents: [1],
			sheetInitialDetentIndex: 0,
			sheetGrabberVisible: false,
		});
	});

	it("refuses to open at a size it can't rest at", () => {
		expect(() => sheetOptions(["large"], "medium")).toThrow("A sheet can't open at medium: it rests only at large.");
		expect(() => sheetOptions([], "large")).toThrow("A sheet can't open at large: it rests only at no size.");
	});

	it("knows which routes are sheets", () => {
		expect(isSheetRoute("TasksSheet")).toBe(true);
		expect(isSheetRoute("Conversation")).toBe(false);
		expect(isSheetRoute("toString")).toBe(false);
		expect(SHEET_ROUTES.TasksSheet).toEqual(sheetOptions(["medium", "large"], "medium"));
		expect(isSheetRoute("NotesSheet")).toBe(true);
		// Notes & links opens at large (spec 8.8) and drags to half.
		expect(SHEET_ROUTES.NotesSheet).toEqual(sheetOptions(["medium", "large"], "large"));
	});
});

describe("the screen in front", () => {
	const board = { key: "board", name: "Sessions" };
	const session = { key: "session", name: "Conversation" };
	const tasks = { key: "tasks", name: "TasksSheet" };
	const reader = { key: "reader", name: "Reader" };

	it("is the focused route", () => {
		const state = { index: 1, routes: [board, session] };
		expect(inFront(state, "session")).toBe(true);
		expect(inFront(state, "board")).toBe(false);
	});

	it("stays in front under its own sheets", () => {
		const state = { index: 2, routes: [board, session, tasks] };
		expect(inFront(state, "session")).toBe(true);
		expect(inFront(state, "tasks")).toBe(true);
		expect(inFront(state, "board")).toBe(false);
	});

	it("leaves the front when a screen is pushed over it, even over a sheet", () => {
		const state = { index: 3, routes: [board, session, tasks, reader] };
		expect(inFront(state, "session")).toBe(false);
		expect(inFront(state, "reader")).toBe(true);
	});

	it("is never in front once it's gone, or while it sits past the index", () => {
		expect(inFront({ index: 1, routes: [board, session] }, "gone")).toBe(false);
		expect(inFront({ index: 0, routes: [board, session] }, "session")).toBe(false);
	});
});
