import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { NotificationCard } from "./NotificationCard";
import type { ParsedNotification } from "./steeringClassify";

afterEach(cleanup);

function notif(overrides: Partial<ParsedNotification> = {}): ParsedNotification {
  return {
    type: "job",
    title: "Job completed",
    tone: "success",
    secondary: "delegate",
    excerpt: "",
    concerns: [],
    rawText: '<job-notification job_id="j">raw</job-notification>',
    ...overrides,
  };
}

test("renders the title and tags the tone", () => {
  render(<NotificationCard notification={notif()} />);
  expect(screen.getByText("Job completed")).toBeTruthy();
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("success");
});

test("a success/neutral notification recedes: no tone chip (color spent only on warning/error)", () => {
  render(<NotificationCard notification={notif({ tone: "success" })} />);
  expect(screen.queryByText("error")).toBe(null);
  expect(screen.queryByText("warning")).toBe(null);
});

test("an error notification earns a danger chip", () => {
  render(<NotificationCard notification={notif({ tone: "error" })} />);
  expect(screen.getByText("error")).toBeTruthy();
});

test("the secondary line surfaces the demoted metadata", () => {
  render(<NotificationCard notification={notif({ secondary: "shell · exit 2 · boom" })} />);
  expect(screen.getByText("shell · exit 2 · boom")).toBeTruthy();
});

test("the raw block is always kept inspectable", () => {
  render(
    <NotificationCard
      notification={notif({ rawText: '<job-notification job_id="abc">the raw text</job-notification>' })}
    />,
  );
  expect(screen.getByTestId("notification-raw").textContent).toContain("the raw text");
});

test("an excerpt is shown, entity-decoded, as escaped text (never live HTML)", () => {
  render(<NotificationCard notification={notif({ excerpt: "&lt;script&gt;alert(1)&lt;/script&gt;" })} />);
  // Decoded to <script>… but rendered as text, so it appears verbatim and no
  // script element is ever created.
  expect(screen.getByText("<script>alert(1)</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBe(null);
});

test("a very long excerpt collapses to a bounded preview plus a full disclosure", () => {
  const long = "x".repeat(900);
  render(<NotificationCard notification={notif({ excerpt: long })} />);
  expect(screen.getByText(/x{500}…/)).toBeTruthy();
});

test("a communicate message renders through markdown", () => {
  const { container } = render(<NotificationCard notification={notif({ message: "**bold** result" })} />);
  expect(container.querySelector("strong")?.textContent).toBe("bold");
});

test("concerns surface as a quiet note", () => {
  render(<NotificationCard notification={notif({ concerns: ["edge case A", "edge case B"] })} />);
  expect(screen.getByText(/edge case A; edge case B/)).toBeTruthy();
});
