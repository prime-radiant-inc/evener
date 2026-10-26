import { expect, test } from "vitest";
import { fenceTokenTerminated, isMermaidCodeToken, MERMAID_FENCE, mermaidText } from "./markdownFence";

test("the gate matches a mermaid opener with indentation, either fence character and any case", () => {
  expect(MERMAID_FENCE.test("intro\n```mermaid\ngraph TD\n```")).toBe(true);
  expect(MERMAID_FENCE.test("   ~~~Mermaid title\nx")).toBe(true);
  expect(MERMAID_FENCE.test("```js\nconst mermaid = 1\n```")).toBe(false);
});

test("a code token is mermaid by its first info word, case-insensitively", () => {
  expect(isMermaidCodeToken({ type: "code", lang: "Mermaid title" })).toBe(true);
  expect(isMermaidCodeToken({ type: "code", lang: "js" })).toBe(false);
  expect(isMermaidCodeToken({ type: "code" })).toBe(false);
  expect(isMermaidCodeToken({ type: "paragraph", lang: "mermaid" })).toBe(false);
});

test("the diagram source gets its stripped trailing newline back", () => {
  expect(mermaidText({ type: "code", text: "graph TD; A-->B" })).toBe("graph TD; A-->B\n");
  expect(mermaidText({ type: "code", text: "" })).toBe("");
  expect(mermaidText({ type: "code" })).toBe("");
});

test.each([
  ["```mermaid\ngraph TD\n```", true],
  ["```mermaid\ngraph TD\n```\n", true],
  ["```mermaid\ngraph TD", false],
  ["````mermaid\nx\n```", false],
  ["```mermaid\nx\n~~~", false],
  ["~~~mermaid\r\nx\r\n~~~", true],
  ["not a fence", false],
])("fence termination of %j is %s", (raw, terminated) => {
  expect(fenceTokenTerminated(raw)).toBe(terminated);
});
