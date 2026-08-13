import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import { RecommendationCard } from "./index";
import rawStyles from "./recommendationcard.module.css";

afterEach(cleanup);

const styles = {
  card: requireClass(rawStyles.card, "recommendationcard.module.css", "card"),
  meterFill: requireClass(rawStyles.meterFill, "recommendationcard.module.css", "meterFill"),
};

test("renders title and body", () => {
  render(<RecommendationCard title="Bump the worker pool" body="Traffic is trending up 3x over baseline." />);
  expect(screen.getByText("Bump the worker pool")).toBeTruthy();
  expect(screen.getByText("Traffic is trending up 3x over baseline.")).toBeTruthy();
});

test("shows the RECOMMENDATION eyebrow", () => {
  render(<RecommendationCard title="Bump the worker pool" body="Traffic is trending up." />);
  expect(screen.getByText("RECOMMENDATION")).toBeTruthy();
});

test("omits the confidence readout when confidence is not given", () => {
  render(<RecommendationCard title="Bump the worker pool" body="Traffic is trending up." />);
  expect(screen.queryByText(/confident/)).toBeNull();
});

test("renders confidence as a rounded percentage caption", () => {
  render(<RecommendationCard title="t" body="b" confidence={0.874} />);
  expect(screen.getByText("87% confident")).toBeTruthy();
});

test("renders the confidence meter fill width from the confidence value", () => {
  const { container } = render(<RecommendationCard title="t" body="b" confidence={0.5} />);
  const fill = container.querySelector(`.${styles.meterFill}`) as HTMLElement;
  expect(fill.style.getPropertyValue("--fill")).toBe("50%");
});

test("clamps an out-of-range confidence into 0-100%", () => {
  const { container } = render(<RecommendationCard title="t" body="b" confidence={1.4} />);
  const fill = container.querySelector(`.${styles.meterFill}`) as HTMLElement;
  expect(fill.style.getPropertyValue("--fill")).toBe("100%");
});

test("omits Accept/Dismiss when their handlers are not given", () => {
  render(<RecommendationCard title="t" body="b" />);
  expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull();
});

test("Accept button fires onAccept", () => {
  const onAccept = () => {
    called = true;
  };
  let called = false;
  render(<RecommendationCard title="t" body="b" onAccept={onAccept} />);
  fireEvent.click(screen.getByRole("button", { name: "Accept" }));
  expect(called).toBe(true);
});

test("Dismiss button fires onReject", () => {
  let called = false;
  render(<RecommendationCard title="t" body="b" onReject={() => (called = true)} />);
  fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
  expect(called).toBe(true);
});

test("renders each alternative as a selectable quiet button", () => {
  let selected = "";
  render(
    <RecommendationCard
      title="t"
      body="b"
      alternatives={[
        { label: "Scale down instead", onSelect: () => (selected = "down") },
        { label: "Do nothing", onSelect: () => (selected = "nothing") },
      ]}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Do nothing" }));
  expect(selected).toBe("nothing");
});

test("renders no alternatives list when none are given", () => {
  render(<RecommendationCard title="t" body="b" />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("carries the card class on its root", () => {
  const { container } = render(<RecommendationCard title="t" body="b" />);
  expect(container.firstElementChild?.classList.contains(styles.card)).toBe(true);
});
