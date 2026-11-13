// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  findIndexIn,
  findMatches,
  findNeedle,
  findReadable,
  itemFindTexts,
  matchPosition,
  stepMatch,
  textMatchesFind,
} from "./findInSession";
import type { ItemModel } from "./model";

function item(fields: Partial<ItemModel>): ItemModel {
  return { id: "item", turnId: "turn", type: "agentMessage", text: "", ...fields } as ItemModel;
}

describe("what counts as a find match", () => {
  it("ignores case and the shape of whitespace on both sides", () => {
    const needle = findNeedle("  Flaky\n  SETTLE ");
    expect(textMatchesFind("the flaky\tsettle test", needle)).toBe(true);
    expect(textMatchesFind("the flaky\n\nsettle test", needle)).toBe(true);
    expect(textMatchesFind("the flakysettle test", needle)).toBe(false);
  });

  it("matches nothing for a query with no words", () => {
    expect(findNeedle(" \n\t")).toBe("");
    expect(textMatchesFind("anything at all", findNeedle("   "))).toBe(false);
    expect(findMatches(["anything"], "  ", (row) => row)).toEqual([]);
  });

  it("locates the match in the text a snippet is cut from", () => {
    const readable = findReadable("  One\n\nTwo  THREE ");
    const at = findIndexIn(readable, findNeedle("two three"));
    expect(readable.slice(at, at + "two three".length).toLowerCase()).toBe("two three");
  });
});

describe("the text an item is searched by", () => {
  it("covers a message, a thought, and a tool call's request and result", () => {
    const tool = item({
      type: "toolCall",
      description: "Run the agent tests",
      argumentsJSON: JSON.stringify({ command: "go test ./agent/...", timeout: 30 }),
      output: "ok agent 1.2s",
      error: "exit status 1",
      reasoningSummaries: [["Thinking ", "about races"]],
    });
    const texts = itemFindTexts(tool);
    const found = (query: string) => texts.some((text) => textMatchesFind(text, findNeedle(query)));
    expect(found("agent tests")).toBe(true);
    expect(found("go test ./agent")).toBe(true);
    expect(found("ok agent")).toBe(true);
    expect(found("exit status")).toBe(true);
    expect(found("thinking about races")).toBe(true);
    // An argument's name is plumbing, not something the row shows.
    expect(found("timeout")).toBe(false);
    expect(itemFindTexts(item({ text: "hello" }))).toEqual(["hello"]);
  });

  it("reads the text inside nested arguments, such as a question's wording and options", () => {
    const ask = item({
      type: "toolCall",
      argumentsJSON: JSON.stringify({
        questions: [{ header: "Deploy", question: "Ship the quokka build?", options: [{ label: "Hold it" }] }],
      }),
    });
    const found = (query: string) => itemFindTexts(ask).some((text) => textMatchesFind(text, findNeedle(query)));
    expect(found("ship the quokka build")).toBe(true);
    expect(found("hold it")).toBe(true);
    // Still by value: the names of the fields are plumbing.
    expect(found("options")).toBe(false);
    expect(found("question")).toBe(false);
  });

  it("survives arguments that are not an object", () => {
    expect(itemFindTexts(item({ argumentsJSON: "not json", text: "kept" }))).toEqual(["kept"]);
  });
});

describe("stepping between matches", () => {
  const matches = [2, 5, 9];

  it("starts at the newest and walks older, then stops", () => {
    expect(stepMatch(matches, null, -1)).toBe(9);
    expect(stepMatch(matches, 9, -1)).toBe(5);
    expect(stepMatch(matches, 2, -1)).toBeNull();
    expect(stepMatch(matches, 5, 1)).toBe(9);
    expect(stepMatch(matches, 9, 1)).toBeNull();
    expect(stepMatch([], null, -1)).toBeNull();
  });

  it("steps from a current row that is no longer a match", () => {
    expect(stepMatch(matches, 6, -1)).toBe(5);
    expect(stepMatch(matches, 6, 1)).toBe(9);
  });

  it("reports the position oldest first", () => {
    expect(matchPosition(matches, 5)).toEqual({ position: 2, count: 3 });
    expect(matchPosition(matches, null)).toEqual({ position: 3, count: 3 });
    expect(matchPosition([], null)).toEqual({ position: 0, count: 0 });
  });
});
