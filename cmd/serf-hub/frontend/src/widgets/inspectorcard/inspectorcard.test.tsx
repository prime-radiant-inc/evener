import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import { InspectorCard } from "./index";
import rawStyles from "./inspectorcard.module.css";

afterEach(cleanup);

const styles = {
  card: requireClass(rawStyles.card, "inspectorcard.module.css", "card"),
  header: requireClass(rawStyles.header, "inspectorcard.module.css", "header"),
};

test("renders the title in a header band", () => {
  const { container } = render(<InspectorCard title="Fine-tune" properties={[]} />);
  const header = container.querySelector(`.${styles.header}`);
  expect(header?.textContent).toContain("Fine-tune");
});

test("renders a read-only property as a mono value", () => {
  render(<InspectorCard title="Fine-tune" properties={[{ key: "epochs", label: "Epochs", value: "3" }]} />);
  expect(screen.getByText("Epochs")).toBeTruthy();
  expect(screen.getByText("3")).toBeTruthy();
  expect(screen.queryByRole("combobox")).toBeNull();
});

test("renders a property with options as a Select", () => {
  let picked = "";
  render(
    <InspectorCard
      title="Fine-tune"
      properties={[
        {
          key: "strategy",
          label: "Strategy",
          value: "lora",
          options: ["lora", "full"],
          onChange: (value) => {
            picked = value;
          },
        },
      ]}
    />,
  );
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  expect(select.value).toBe("lora");
  fireEvent.change(select, { target: { value: "full" } });
  expect(picked).toBe("full");
});

test("renders one row per property", () => {
  const { container } = render(
    <InspectorCard
      title="Fine-tune"
      properties={[
        { key: "epochs", label: "Epochs", value: "3" },
        { key: "lr", label: "Learning rate", value: "0.0002" },
      ]}
    />,
  );
  expect(container.querySelectorAll('[data-testid="inspector-row"]').length).toBe(2);
});

test("carries the card class on its root", () => {
  const { container } = render(<InspectorCard title="Fine-tune" properties={[]} />);
  expect(container.firstElementChild?.classList.contains(styles.card)).toBe(true);
});
