import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test } from "vitest";
import { Textarea } from "../textarea";
import { PromptCard } from "./index";

afterEach(cleanup);

function moduleCss(): string {
  return readFileSync(join(dirname(fileURLToPath(import.meta.url)), "promptcard.module.css"), "utf8");
}

/** A live card wrapping a controlled seamless field, the shape both real
 * callers (the session composer and the spawn form) render. */
function LiveCard(props: { leading?: React.ReactNode; actions?: React.ReactNode; hidden?: boolean }) {
  const [value, setValue] = useState("");
  return (
    <PromptCard
      data-testid="card"
      hidden={props.hidden}
      leading={props.leading}
      actions={props.actions}
      field={
        <Textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          autoGrow
          seamless
          aria-label="Message"
          placeholder="Message the agent…"
        />
      }
    />
  );
}

test("renders the caller's field", () => {
  render(<LiveCard />);
  expect(screen.getByRole("textbox", { name: "Message" })).toBeTruthy();
});

test("typing in the field works through the card", async () => {
  const user = userEvent.setup();
  render(<LiveCard />);
  await user.type(screen.getByRole("textbox", { name: "Message" }), "hello");
  expect((screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement).value).toBe("hello");
});

test("renders the caller's leading and action controls", () => {
  render(
    <LiveCard
      leading={<button type="button">Attach</button>}
      actions={
        <>
          <button type="button">Stop</button>
          <button type="button">Send</button>
        </>
      }
    />,
  );
  expect(screen.getByRole("button", { name: "Attach" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Stop" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
});

// The action cluster's DOM order IS its visual order (the row is a plain flex
// row, no reordering), so a caller that puts Stop first gets Stop leftmost -
// which is the composer's whole reason for wanting the slot.
test("action controls keep the caller's order inside the trailing cluster", () => {
  render(
    <LiveCard
      actions={
        <>
          <button type="button">Stop</button>
          <button type="button">Send</button>
          <button type="button">Steer</button>
        </>
      }
    />,
  );
  const labels = [...screen.getByTestId("card").querySelectorAll("button")].map((b) => b.textContent);
  expect(labels).toEqual(["Stop", "Send", "Steer"]);
});

// A card with no controls at all is a legitimate shape (a finished session's
// collapsed follow-up affordance) - it must not render an empty control row
// reserving vertical space for nothing.
test("renders no control row at all when neither slot is supplied", () => {
  render(<LiveCard />);
  const card = screen.getByTestId("card");
  expect(card.children).toHaveLength(1); // the field alone
});

test("hidden takes the card out of the interaction tree, not just out of sight", () => {
  render(<LiveCard hidden actions={<button type="button">Send</button>} />);
  const card = screen.getByTestId("card");
  expect(card.hasAttribute("hidden")).toBe(true);
  expect(card.hasAttribute("inert")).toBe(true);
});

// The seamless field inside draws no ring of its own (widgets/textarea's
// `seamless` drops it), so the card MUST carry the focus affordance or focusing
// the field would show nothing at all. jsdom applies no stylesheet, so the rule
// is checked against the stylesheet text and the state against the DOM.
test("the card owns the focus ring for the seamless field inside it", () => {
  expect(moduleCss()).toMatch(/\.card:focus-within\s*\{[^}]*outline: 2px solid var\(--accent\)/);
});

// Queried only after focusing: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the field puts the card in :focus-within", () => {
  render(<LiveCard />);
  screen.getByRole("textbox", { name: "Message" }).focus();
  expect(screen.getByTestId("card").matches(":focus-within")).toBe(true);
});

test("the card draws exactly one hairline border, so a seamless field inside is not a box in a box", () => {
  const rule = /\.card\s*\{([^}]*)\}/.exec(moduleCss())?.[1] ?? "";
  expect(rule).toMatch(/border:\s*1px solid var\(--edge\)/);
});
