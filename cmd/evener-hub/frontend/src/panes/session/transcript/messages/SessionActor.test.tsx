import type { ItemModel } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { SteeringItem } from "./SteeringItem";
import { UserMessageView } from "./UserMessageItem";

const base: ItemModel = { id: "item_1", turnId: "turn_1", type: "userMessage", text: "same input" };
const managerRef = "local:manager-origin";

test("SessionActor uses actor kind to select the supplied speaker identity", () => {
  const speaker = "speaker-origin-data";
  const { rerender } = render(
    <UserMessageView item={{ ...base, actor: { kind: "human", managerRef } }} name={speaker} />,
  );
  expect(screen.getByText(speaker)).toBeTruthy();
  expect(screen.queryByText(managerRef, { exact: false })).toBeNull();

  rerender(<UserMessageView item={{ ...base, actor: { kind: "manager", managerRef } }} name={speaker} />);
  expect(screen.getByText(managerRef, { exact: false })).toBeTruthy();
  expect(screen.queryByText(speaker)).toBeNull();
});

test("SessionActor preserves the supplied Manager identity through steering", () => {
  render(
    <SteeringItem
      item={{
        ...base,
        id: "steer_1",
        type: "steering",
        source: "user",
        actor: { kind: "manager", managerRef },
      }}
      sessionRef="local:worker"
      turn={{ id: "turn_1", status: "completed", items: [] }}
      live={false}
    />,
  );
  expect(screen.getByText(managerRef, { exact: false })).toBeTruthy();
  expect(screen.getByTestId("user-message-item").getAttribute("data-opens-exchange")).toBeNull();
});
