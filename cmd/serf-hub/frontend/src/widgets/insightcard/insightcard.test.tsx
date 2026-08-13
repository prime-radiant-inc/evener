import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { type Insight, InsightCard } from "./index";

afterEach(cleanup);

const INSIGHTS: Insight[] = [
  { title: "Token spend up", body: "Usage rose 12% week over week.", series: [3, 5, 4, 8, 9] },
  { title: "Idle sessions", body: "Three sessions have been idle for over a day." },
  { title: "Retry rate", body: "Retries dropped after the last deploy.", series: [10, 6, 6, 2] },
];

test("renders the current page's title and body, not other pages'", () => {
  render(<InsightCard insights={INSIGHTS} page={0} onPageChange={vi.fn()} />);
  expect(screen.getByText("Token spend up")).toBeTruthy();
  expect(screen.getByText("Usage rose 12% week over week.")).toBeTruthy();
  expect(screen.queryByText("Idle sessions")).toBeNull();
});

test("renders the requested page when page is not 0", () => {
  render(<InsightCard insights={INSIGHTS} page={1} onPageChange={vi.fn()} />);
  expect(screen.getByText("Idle sessions")).toBeTruthy();
});

test("renders a 1-indexed page counter caption", () => {
  render(<InsightCard insights={INSIGHTS} page={1} onPageChange={vi.fn()} />);
  expect(screen.getByText("2 of 3")).toBeTruthy();
});

test("clicking Previous reports page - 1", () => {
  const onPageChange = vi.fn();
  render(<InsightCard insights={INSIGHTS} page={1} onPageChange={onPageChange} />);
  fireEvent.click(screen.getByRole("button", { name: "Previous insight" }));
  expect(onPageChange).toHaveBeenCalledWith(0);
});

test("clicking Next reports page + 1", () => {
  const onPageChange = vi.fn();
  render(<InsightCard insights={INSIGHTS} page={1} onPageChange={onPageChange} />);
  fireEvent.click(screen.getByRole("button", { name: "Next insight" }));
  expect(onPageChange).toHaveBeenCalledWith(2);
});

test("Previous is disabled on the first page", () => {
  render(<InsightCard insights={INSIGHTS} page={0} onPageChange={vi.fn()} />);
  expect((screen.getByRole("button", { name: "Previous insight" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Next insight" }) as HTMLButtonElement).disabled).toBe(false);
});

test("Next is disabled on the last page", () => {
  render(<InsightCard insights={INSIGHTS} page={2} onPageChange={vi.fn()} />);
  expect((screen.getByRole("button", { name: "Next insight" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Previous insight" }) as HTMLButtonElement).disabled).toBe(false);
});

test("both nav buttons are disabled with zero insights", () => {
  render(<InsightCard insights={[]} page={0} onPageChange={vi.fn()} />);
  expect((screen.getByRole("button", { name: "Previous insight" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Next insight" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByText("0 of 0")).toBeTruthy();
});

test("renders no title/body with zero insights", () => {
  render(<InsightCard insights={[]} page={0} onPageChange={vi.fn()} />);
  expect(screen.queryByTestId("insightcard-title")).toBeNull();
  expect(screen.queryByTestId("insightcard-body")).toBeNull();
});

test("renders a sparkline svg when the page's insight has a series", () => {
  render(<InsightCard insights={INSIGHTS} page={0} onPageChange={vi.fn()} />);
  expect(screen.getByTestId("insightcard-chart")).toBeTruthy();
});

test("renders no sparkline when the page's insight has no series", () => {
  render(<InsightCard insights={INSIGHTS} page={1} onPageChange={vi.fn()} />);
  expect(screen.queryByTestId("insightcard-chart")).toBeNull();
});

test("renders no sparkline for an empty series", () => {
  render(<InsightCard insights={[{ title: "x", body: "y", series: [] }]} page={0} onPageChange={vi.fn()} />);
  expect(screen.queryByTestId("insightcard-chart")).toBeNull();
});

test("the sparkline svg is aria-hidden", () => {
  render(<InsightCard insights={INSIGHTS} page={0} onPageChange={vi.fn()} />);
  expect(screen.getByTestId("insightcard-chart").getAttribute("aria-hidden")).toBe("true");
});

test("the sparkline carries a text alternative built from the series' min and max", () => {
  render(<InsightCard insights={INSIGHTS} page={0} onPageChange={vi.fn()} />);
  // INSIGHTS[0].series = [3, 5, 4, 8, 9] -> min 3, max 9
  expect(screen.getByText(/3.*9|9.*3/)).toBeTruthy();
});

test("the sparkline path is scaled to the series' own point count via its viewBox", () => {
  render(<InsightCard insights={INSIGHTS} page={2} onPageChange={vi.fn()} />);
  // INSIGHTS[2].series has 4 points -> viewBox width tracks (length - 1) = 3
  const svg = screen.getByTestId("insightcard-chart");
  const viewBox = svg.getAttribute("viewBox")!;
  const [, , width] = viewBox.split(" ").map(Number);
  expect(width).toBe(3);
});
