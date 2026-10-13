// Pins the shellguard hover-card FAILURE witness (#3741). The witness runs only
// when a journey throws. If the witness itself throws - because the title, row
// or rail it dereferences is missing, exactly the state a failing guard is in -
// then `evaluate` rejects and the guard's real failure is replaced by the
// witness's own TypeError: the operator loses both the real error and the
// diagnostic they needed.
//
// These cases drive the SAME exported `describeHoverFailure` the `onPage` catch
// calls, through the REAL `evaluate` and the REAL `hoverWitnessExpr`, with a fake
// CDP send that runs the page expression in jsdom. The fake send stubs only the
// browser transport (the pattern in scripts/browserGuardCdp.test.js); the
// expression under test is the guard's own. No browser.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { describeHoverFailure, hoverWitnessExpr } from "./sessionHoverCard.mjs";

// Evaluate a page expression the way the guard's evaluate does, in jsdom.
function evaluateInPage(expression) {
  return new Function(`return (${expression});`)();
}

// A fake CDP `send`: Runtime.evaluate runs the page expression and returns the
// response shape `evaluate` unwraps.
function pageSend() {
  return vi.fn(async (method, params) => {
    if (method !== "Runtime.evaluate") throw new Error(`unexpected CDP method ${method}`);
    return { result: { result: { value: evaluateInPage(params.expression) } } };
  });
}

// A present guard whose diagnostic elements are missing - the state a real
// journey reaches when it reassigns state.title/state.row (desktopJourney
// ~lines 233-250) to a null query result. The helpers dereference their element
// the way the guard's own page code does, so a missing element is really
// unreadable: `box = element => element.getBoundingClientRect().toJSON()`
// throws on null (jsdom's DOMRect has no toJSON, so the same plain rect is
// assembled here), and `card()` reads this.title.
function installGuard(overrides = {}) {
  window.__sessionHoverGuard = {
    title: document.createElement("div"),
    row: document.createElement("div"),
    rail: document.createElement("div"),
    box: (element) => {
      const rect = element.getBoundingClientRect();
      return {
        x: rect.x,
        y: rect.y,
        width: rect.width,
        height: rect.height,
        top: rect.top,
        right: rect.right,
        bottom: rect.bottom,
        left: rect.left,
      };
    },
    card() {
      return document.getElementById(this.title.getAttribute("aria-describedby"));
    },
    ...overrides,
  };
}

// The composer appends the witness JSON after `; witness `. Asserting the exact
// prefix is half the contract: the guard's own message, then the witness.
function witnessOf(failure, original) {
  const prefix = `${original.message}; witness `;
  if (!failure.message.startsWith(prefix)) {
    throw new Error(`failure message lost the original error: ${failure.message}`);
  }
  return JSON.parse(failure.message.slice(prefix.length));
}

beforeEach(() => {
  document.body.innerHTML = "";
  window.__shellGuardErrors = [];
  // jsdom has no media queries or hit test; stub the two browser APIs it lacks,
  // as scripts/skillguard/run.test.mjs already does for elementFromPoint.
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  document.elementFromPoint = () => null;
});

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, "__sessionHoverGuard");
  Reflect.deleteProperty(window, "__shellGuardErrors");
  Reflect.deleteProperty(document, "elementFromPoint");
});

describe("hover-card failure witness", () => {
  test("the witness expression returns instead of throwing when the title is absent", () => {
    installGuard({ title: null });
    const witness = evaluateInPage(hoverWitnessExpr());
    expect(witness.partial).toBe(true);
    expect(witness.unreadable).toEqual(expect.arrayContaining(["title"]));
  });

  test("a missing diagnostic element keeps the original error and partial evidence", async () => {
    installGuard({ title: null, row: null, rail: null });
    const original = new Error("session hover card 1400px 200 dark: card escapes viewport clearance");
    const failure = await describeHoverFailure({ send: pageSend() }, original);

    expect(failure).toBeInstanceOf(Error);
    expect(failure.cause).toBe(original);
    const witness = witnessOf(failure, original);
    expect(witness.partial).toBe(true);
    expect(witness.unreadable).toEqual(expect.arrayContaining(["title", "row", "sidebar", "card", "hit", "hovered"]));
    expect(witness.errors).toEqual([]);
  });

  test("a witness read that cannot run at all is marked, not raised", () => {
    // No media query or hit-test support, as in a plain jsdom or a torn-down
    // page: the guard must still return the rest of the evidence.
    vi.unstubAllGlobals();
    Reflect.deleteProperty(document, "elementFromPoint");
    installGuard();
    const witness = evaluateInPage(hoverWitnessExpr());
    expect(witness.partial).toBe(true);
    expect(witness.unreadable).toEqual(expect.arrayContaining(["hoverless", "hit"]));
    expect(witness.title).toBe("<div></div>");
  });

  test("a guard that was never installed reports only the page errors", () => {
    const witness = evaluateInPage(hoverWitnessExpr());
    expect(witness).toEqual({ partial: true, unreadable: ["state"], errors: [] });
  });

  test("a failed witness collection is a note, never a replacement error", async () => {
    installGuard();
    const original = new Error("session hover card 390px drawer dark: long press opened no card");
    const send = vi.fn(async () => {
      throw new Error("Runtime.evaluate transport lost");
    });
    const failure = await describeHoverFailure({ send }, original);

    expect(failure).toBeInstanceOf(Error);
    expect(failure.cause).toBe(original);
    const witness = witnessOf(failure, original);
    expect(witness.partial).toBe(true);
    expect(witness.unreadable).toEqual(["witness"]);
    expect(witness.error).toContain("Runtime.evaluate transport lost");
  });

  test("full evidence still reaches the operator when every element is present", async () => {
    installGuard();
    const original = new Error("session hover card 1400px 560 light: card is not row-centered");
    const failure = await describeHoverFailure({ send: pageSend() }, original);

    const witness = witnessOf(failure, original);
    expect(witness.partial).toBe(false);
    expect(witness.unreadable).toEqual([]);
    expect(witness.hoverless).toBe(false);
    expect(witness.title).toBe("<div></div>");
    expect(witness.row).toMatchObject({ width: 0, height: 0 });
    expect(witness.sidebar).toMatchObject({ width: 0, height: 0 });
    expect(witness.card).toBeNull();
    expect(witness.hit).toBeNull();
    expect(witness.errors).toEqual([]);
  });
});
