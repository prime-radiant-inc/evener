import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, test, vi } from "vitest";
import { DisclosureHeading } from "./index";

test("renders a heading at the given level whose name is the label", () => {
  render(<DisclosureHeading level={3} label="Live" open onOpenChange={() => {}} />);
  const heading = screen.getByRole("heading", { level: 3, name: "Live" });
  expect(within(heading).getByRole("button", { name: "Live" })).toBeTruthy();
});

test("the heading's button reports the open state through aria-expanded", () => {
  const { rerender } = render(<DisclosureHeading level={3} label="Live" open onOpenChange={() => {}} />);
  expect(screen.getByRole("button", { name: "Live" }).getAttribute("aria-expanded")).toBe("true");
  rerender(<DisclosureHeading level={3} label="Live" open={false} onOpenChange={() => {}} />);
  expect(screen.getByRole("button", { name: "Live" }).getAttribute("aria-expanded")).toBe("false");
});

test("activating the button asks for the opposite state and keeps focus on it", async () => {
  const user = userEvent.setup();
  const onOpenChange = vi.fn();
  render(<DisclosureHeading level={3} label="Live" open onOpenChange={onOpenChange} />);
  const button = screen.getByRole("button", { name: "Live" });
  await user.click(button);
  expect(onOpenChange).toHaveBeenCalledWith(false);
  expect(document.activeElement).toBe(button);
});

test("Enter and Space toggle a controlled heading", async () => {
  const user = userEvent.setup();
  function Harness() {
    const [open, setOpen] = useState(false);
    return <DisclosureHeading level={2} label="Archived" open={open} onOpenChange={setOpen} />;
  }
  render(<Harness />);
  const button = screen.getByRole("button", { name: "Archived" });
  button.focus();
  await user.keyboard("{Enter}");
  expect(button.getAttribute("aria-expanded")).toBe("true");
  await user.keyboard(" ");
  expect(button.getAttribute("aria-expanded")).toBe("false");
});

test("the chevron trails the label and is hidden from assistive tech", () => {
  render(<DisclosureHeading level={3} label="Live" open onOpenChange={() => {}} />);
  const button = screen.getByRole("button", { name: "Live" });
  const chevron = button.lastElementChild;
  expect(chevron?.tagName.toLowerCase()).toBe("svg");
  expect(chevron?.getAttribute("aria-hidden")).toBe("true");
  expect(button.firstChild?.nodeType).toBe(Node.TEXT_NODE);
});

test("an eyebrow label is wrapped as a group label while a plain label stays bare text", () => {
  render(<DisclosureHeading level={3} label="Projects" eyebrow open onOpenChange={() => {}} />);
  const button = screen.getByRole("button", { name: "Projects" });
  expect(button.firstChild?.nodeType).toBe(Node.ELEMENT_NODE);
  expect(button.firstChild?.contains(button.querySelector("svg"))).toBe(false);
});

test("the action renders beside the heading, outside its button", async () => {
  const user = userEvent.setup();
  const onAction = vi.fn();
  const onOpenChange = vi.fn();
  render(
    <DisclosureHeading
      level={3}
      label="Pins"
      open
      onOpenChange={onOpenChange}
      action={
        <button type="button" onClick={onAction}>
          Section actions
        </button>
      }
    />,
  );
  const heading = screen.getByRole("heading", { name: "Pins" });
  const action = screen.getByRole("button", { name: "Section actions" });
  expect(heading.contains(action)).toBe(false);
  await user.click(action);
  expect(onAction).toHaveBeenCalledTimes(1);
  expect(onOpenChange).not.toHaveBeenCalled();
});
