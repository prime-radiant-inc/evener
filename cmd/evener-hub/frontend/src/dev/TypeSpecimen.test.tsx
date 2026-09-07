import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import TypeSpecimen from "./TypeSpecimen";

afterEach(cleanup);

// The whole specimen is wrapped in ThemeFlip, which renders its children
// once per theme, so every label on the page appears exactly twice.
// Asserting the count (rather than getByText, which throws on the second
// match) is what pins "both themes render" - the point of the route.
const THEMES = 2;

const RAMP_LABELS = ["caption 12", "ui 13", "body 15", "pane-title 18", "page-title 22", "display 28"];
const RHYTHM_LABELS = ["rhythm-line 4", "rhythm-item 8", "rhythm-group 16", "rhythm-exchange 24"];
const LEADING_LABELS = ["line-height-ui", "line-height-body", "line-height-title"];
const MEASURE_LABELS = ["44rem", "64rem"];

test("renders without throwing, with the intro note", () => {
  render(<TypeSpecimen />);
  expect(screen.getByText(/type specimen/i)).toBeTruthy();
});

test.each(RAMP_LABELS)("shows the ramp step %s in both themes", (label) => {
  render(<TypeSpecimen />);
  expect(screen.getAllByText(label)).toHaveLength(THEMES);
});

test.each(LEADING_LABELS)("shows the line-height sample %s in both themes", (label) => {
  render(<TypeSpecimen />);
  expect(screen.getAllByText(label)).toHaveLength(THEMES);
});

test.each(RHYTHM_LABELS)("shows the rhythm step %s in both themes", (label) => {
  render(<TypeSpecimen />);
  expect(screen.getAllByText(label)).toHaveLength(THEMES);
});

test.each(MEASURE_LABELS)("shows a paragraph at the %s measure in both themes", (label) => {
  render(<TypeSpecimen />);
  expect(screen.getAllByText(label)).toHaveLength(THEMES);
});

test("shows the eyebrow recipe rendered in itself", () => {
  render(<TypeSpecimen />);
  expect(screen.getAllByText("Recommended")).toHaveLength(THEMES);
});

test("the measure paragraph is the same 600-character text in both columns", () => {
  render(<TypeSpecimen />);
  const paragraphs = screen.getAllByText(/A session is a conversation with an agent/);
  // one per measure, per theme
  expect(paragraphs).toHaveLength(2 * THEMES);
  for (const paragraph of paragraphs) {
    expect(paragraph.textContent?.length).toBe(600);
  }
});
