import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./useSkillTool";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

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
  const output = "Skill: test-driven-development\nLocation: /skills/tdd\n\n---\n\nWrite the failing test first.";
  render(<Body item={item({ toolName: "use_skill", output })} live={false} />);
  expect(screen.getByText(/Write the failing test first\./)).toBeTruthy();
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
