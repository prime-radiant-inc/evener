import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { MermaidDiagram } from "./index";
import { installJsdomSvgShims } from "./jsdomSvgShims";

// The timeout case must not depend on how fast the real render happens to be:
// once mermaid is warm from an earlier case, a real render of "A-->B" settles
// before a 1ms timer ever gets the event loop, so the race is decided by
// scheduling, not by the timeout wrapper. To pin the wrapper itself, the one
// timeout case swaps in a render that never settles - the fake boundary here is
// the component's own render port (renderMermaidSvg), the "external" call the
// timeout exists to bound, never the wrapper under test. Every other case runs
// the real security pipeline. vi.mock is hoisted, so the controllable
// implementation lives in vi.hoisted.
const renderPort = vi.hoisted(() => ({
  impl: null as null | ((source: string, themeVariables: Record<string, string>) => Promise<string>),
}));

vi.mock("./security", async () => {
  const actual = await vi.importActual<typeof import("./security")>("./security");
  return {
    ...actual,
    renderMermaidSvg: (source: string, themeVariables: Record<string, string>) =>
      renderPort.impl ? renderPort.impl(source, themeVariables) : actual.renderMermaidSvg(source, themeVariables),
  };
});

// A real mermaid render in jsdom costs roughly half a second, and jsdom has no
// SVG layout (jsdomSvgShims supplies the missing measurement APIs). This file
// keeps its render count small - one benign flowchart plus the failure paths -
// so each case carries a generous per-test ceiling that is a tripwire for a
// genuine hang, never the mechanism.
beforeAll(() => installJsdomSvgShims());
afterEach(() => {
  cleanup();
  renderPort.impl = null;
});

describe("MermaidDiagram", () => {
  it("renders a real flowchart with its labels", async () => {
    render(<MermaidDiagram source={"graph TD; A[Start node]-->B{Decision label}"} />);
    await waitFor(() => expect(document.querySelector("[data-mermaid-diagram] svg")).not.toBeNull(), {
      timeout: 15000,
    });
    expect(screen.getByText("Start node")).toBeTruthy();
    expect(screen.getByText("Decision label")).toBeTruthy();
  }, 20000);

  it("falls back to the source with an error note on invalid mermaid", async () => {
    render(<MermaidDiagram source={"graph TD; A[unclosed"} />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), {
      timeout: 15000,
    });
    expect(screen.getByText(/graph TD/)).toBeTruthy();
  }, 20000);

  it("renders no anchor for an authored link label or click directive", async () => {
    render(
      <MermaidDiagram
        source={
          'graph TD; A["<a href=\'https://evil.example\'>click</a>"] --> B; click B href "https://evil.example/click";'
        }
      />,
    );
    await waitFor(() => expect(document.querySelector("[data-mermaid-diagram] svg")).not.toBeNull(), {
      timeout: 15000,
    });
    expect(document.querySelector("[data-mermaid-diagram] a")).toBeNull();
  }, 20000);

  it("times a wedged render out to the error fallback", async () => {
    renderPort.impl = () => new Promise<string>(() => {});
    render(<MermaidDiagram source={"graph TD; A-->B"} renderTimeoutMs={1} />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), { timeout: 15000 });
  }, 20000);

  it("hits the error fallback for an empty diagram", async () => {
    render(<MermaidDiagram source="" />);
    await waitFor(() => expect(screen.getByText(/couldn't render this diagram/i)).toBeTruthy(), { timeout: 15000 });
  }, 20000);

  // The Task 7 wiring, pinned without a real mermaid render: the render port
  // stands in for the settled SVG (the boundary the timeout case above also
  // swaps), so this stays fast. A rendered diagram is a focusable button and
  // clicking it opens the internal DiagramViewer.
  it("opens the fullscreen viewer when the rendered diagram is clicked", async () => {
    renderPort.impl = async () => '<svg role="graphics-document document"><text>hi</text></svg>';
    const user = userEvent.setup();
    render(<MermaidDiagram source={"graph TD; A-->B"} />);
    const trigger = await waitFor(() => {
      const button = document.querySelector<HTMLButtonElement>("button[data-mermaid-diagram]");
      expect(button).not.toBeNull();
      return button!;
    });
    // The trigger's only content is the (aria-hidden) SVG, so its name must
    // come from aria-label or a screen reader announces a bare "button".
    expect(screen.getByRole("button", { name: "Open diagram fullscreen" })).toBe(trigger);
    expect(trigger.querySelector('[aria-hidden="true"]')).not.toBeNull();
    await user.click(trigger);
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
