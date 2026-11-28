import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { type TabOption, Tabs } from "./index";

type TabId = "about" | "links" | "agents";

const OPTIONS: readonly TabOption<TabId>[] = [
  { value: "about", label: "About" },
  { value: "links", label: "Links", detail: "12", accessibleLabel: "Links, 12" },
  { value: "agents", label: "Agents", detail: "2/5", accessibleLabel: "Agents, 2 of 5 active" },
];

test("renders a horizontal underline tablist with single- and two-level tabs", () => {
  render(<Tabs label="Overview kind" value="about" options={OPTIONS} onChange={() => {}} panelId="panel-1" />);
  const tablist = screen.getByRole("tablist", { name: "Overview kind" });
  expect(tablist.getAttribute("aria-orientation")).toBe("horizontal");
  const tabs = within(tablist).getAllByRole("tab");
  expect(tabs).toHaveLength(3);
  expect(tabs[0]?.getAttribute("aria-selected")).toBe("true");
  expect(tabs[0]?.tabIndex).toBe(0);
  expect(tabs[0]?.getAttribute("aria-controls")).toBe("panel-1");
  expect(tabs[0]?.firstElementChild?.children).toHaveLength(1);
  expect(tabs[1]?.getAttribute("aria-selected")).toBe("false");
  expect(tabs[1]?.tabIndex).toBe(-1);
  expect(tabs[1]?.firstElementChild?.children).toHaveLength(2);
  expect(tabs[2]?.getAttribute("aria-label")).toBe("Agents, 2 of 5 active");
});

test("clicking a tab and roving with Arrow/Home/End calls onChange and ignores modifier chords", () => {
  const onChange = vi.fn();
  render(<Tabs label="Overview kind" value="about" options={OPTIONS} onChange={onChange} />);
  const about = screen.getByRole("tab", { name: "About" });
  const links = screen.getByRole("tab", { name: "Links, 12" });

  fireEvent.click(links);
  expect(onChange).toHaveBeenLastCalledWith("links");

  onChange.mockClear();
  fireEvent.keyDown(about, { key: "ArrowRight", shiftKey: true });
  fireEvent.keyDown(about, { key: "ArrowRight", altKey: true });
  expect(onChange).not.toHaveBeenCalled();

  fireEvent.keyDown(about, { key: "ArrowRight" });
  expect(onChange).toHaveBeenLastCalledWith("links");

  fireEvent.keyDown(about, { key: "End" });
  expect(onChange).toHaveBeenLastCalledWith("agents");

  fireEvent.keyDown(about, { key: "Home" });
  expect(onChange).toHaveBeenLastCalledWith("about");
});
