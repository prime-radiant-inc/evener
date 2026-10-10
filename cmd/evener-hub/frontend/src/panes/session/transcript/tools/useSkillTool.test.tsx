import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./useSkillTool";
import type { ItemModel } from "@evener/appwire-client";
import { toolWireStep } from "@evener/appwire-client/testing/toolWireFixtures";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test("summary names the activated skill", () => {
  const d = toolRendererFor("use_skill");
  const args = JSON.stringify({ skill_name: "test-driven-development" });
  expect(d.summary(item({ toolName: "use_skill", argumentsJSON: args }))).toBe(
    "Activated skill: test-driven-development",
  );
});

test("body shows the skill's own output text (Skill:/Location:/body, per agent/session_tools_communicate.go)", () => {
  const d = toolRendererFor("use_skill");
  const Body = d.body!;
  const output = [
    "Skill: test-driven-development",
    "Location: /skills/tdd",
    "",
    "---",
    "",
    "# Test-driven development",
    "",
    "- Write the failing test",
    "- Run it and watch it fail",
    "",
    "```go",
    "func TestExample(t *testing.T) {}",
    "```",
  ].join("\n");
  render(<Body item={item({ toolName: "use_skill", output })} live={false} />);
  expect(screen.getByRole("heading", { name: "Test-driven development" })).toBeTruthy();
  expect(screen.getByRole("list")).toBeTruthy();
  expect(screen.getByText("Write the failing test")).toBeTruthy();
  expect(screen.getByText("func TestExample(t *testing.T) {}")).toBeTruthy();
  expect(screen.queryByText("# Test-driven development")).toBeNull();
});

test("body renders nothing when output is blank (the started-before-activated race, agent/internal/appprojector)", () => {
  const d = toolRendererFor("use_skill");
  const Body = d.body!;
  const { container } = render(<Body item={item({ toolName: "use_skill", output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

test("the skill name reads straight from a settled item's own argumentsJSON (the model preserves it through item/completed - see R2)", () => {
  const d = toolRendererFor("use_skill");
  const settled = item({ toolName: "use_skill", argumentsJSON: JSON.stringify({ skill_name: "brainstorming" }) });
  expect(d.summary(settled)).toBe("Activated skill: brainstorming");
});

// What use_skill really returns (agent/testdata/toolwire): a <skill-context>
// block of JSON. The body shows the skill's instructions as markdown, never
// the JSON around them.
test("body shows the loaded skill's instructions, not its <skill-context> JSON", () => {
  const Body = toolRendererFor("use_skill").body!;
  render(<Body item={toolWireStep("call_use_skill")} live={false} />);
  expect(screen.getByRole("heading", { name: "Systematic debugging" })).toBeTruthy();
  expect(screen.getByText("Find the root cause first.")).toBeTruthy();
  expect(screen.queryByText(/skill-context|"instructions"/)).toBeNull();
});
