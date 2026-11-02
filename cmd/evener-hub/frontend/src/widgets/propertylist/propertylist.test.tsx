import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { PropertyList } from "./index";

// jsdom applies no stylesheet, so the declarations are the checkable truth;
// comments are stripped so a doc comment quoting one cannot satisfy a match.
const CSS = readModuleCss(import.meta.url, "propertylist.module.css");

function classesOf(element: Element): string[] {
  return element.className.split(" ").filter(Boolean);
}

test("each row pairs its label term with its value definition, in order", () => {
  render(
    <PropertyList
      rows={[
        { label: "Version", value: "v1.2.0", mono: true },
        { label: "Marketplace", value: "evener-official" },
      ]}
    />,
  );
  expect(screen.getAllByRole("term").map((term) => term.textContent)).toEqual(["Version", "Marketplace"]);
  expect(screen.getAllByRole("definition").map((definition) => definition.textContent)).toEqual([
    "v1.2.0",
    "evener-official",
  ]);
});

test("a value can be rich content", () => {
  render(
    <PropertyList
      rows={[
        {
          label: "Install location",
          value: (
            <>
              <code>~/.evener/plugins</code> not fetched yet
            </>
          ),
        },
      ]}
    />,
  );
  expect(screen.getByRole("definition").textContent).toBe("~/.evener/plugins not fetched yet");
});

test("an empty row list renders no terms", () => {
  render(<PropertyList rows={[]} />);
  expect(screen.queryByRole("term")).toBeNull();
});

test("an inline value truncates by default", () => {
  expect(topRuleBlock(CSS, ".value")).toContain("white-space: nowrap");
  expect(topRuleBlock(CSS, ".value")).toContain("text-overflow: ellipsis");
});

test("a wrap row's value wraps in full instead of truncating, and only that row's", () => {
  render(
    <PropertyList
      rows={[
        { label: "Target", value: "build-box.tailnet.example:22" },
        { label: "Install path", value: "/opt/evener/releases/2026-10-11/evener-daemon", wrap: true },
      ]}
    />,
  );
  const [target, installPath] = screen.getAllByRole("definition");
  expect(classesOf(installPath!)).toEqual(expect.arrayContaining(classesOf(target!)));
  expect(classesOf(installPath!).length).toBe(classesOf(target!).length + 1);
  const wrap = topRuleBlock(CSS, ".wrap");
  expect(wrap).toContain("white-space: normal");
  expect(wrap).toContain("overflow-wrap: anywhere");
  expect(wrap).toContain("overflow: visible");
});

test("eyebrowLabels labels rows with the eyebrow recipe, and only when asked", () => {
  const { rerender } = render(
    <PropertyList rows={[{ label: "Was", value: "https://old.example", mono: true, wrap: true }]} />,
  );
  const plain = classesOf(screen.getByRole("term"));

  rerender(
    <PropertyList eyebrowLabels rows={[{ label: "Was", value: "https://old.example", mono: true, wrap: true }]} />,
  );
  const eyebrow = classesOf(screen.getByRole("term"));
  expect(eyebrow).toEqual(expect.arrayContaining(plain));
  expect(eyebrow).toHaveLength(plain.length + 1);

  const recipe = topRuleBlock(CSS, ".eyebrowLabel");
  expect(recipe).toContain("font-size: var(--font-size-caption)");
  expect(recipe).toContain("text-transform: uppercase");
  expect(recipe).toContain("letter-spacing: var(--tracking-eyebrow)");
  expect(recipe).toContain("font-weight: var(--font-weight-medium)");
});

test("stacked rows put the label over the value and wrap the value", () => {
  const { container, rerender } = render(
    <PropertyList rows={[{ label: "State dir", value: "~/.local/state/evener" }]} />,
  );
  const inlineRowClasses = classesOf(container.querySelector("dt")!.parentElement!);
  const inlineValueClasses = classesOf(screen.getByRole("definition"));

  rerender(<PropertyList layout="stacked" rows={[{ label: "State dir", value: "~/.local/state/evener" }]} />);
  expect(classesOf(container.querySelector("dt")!.parentElement!)).not.toEqual(inlineRowClasses);
  expect(classesOf(screen.getByRole("definition"))).not.toEqual(inlineValueClasses);

  expect(topRuleBlock(CSS, ".stackedRow")).toContain("flex-direction: column");
  expect(topRuleBlock(CSS, ".stackedLabel")).toContain("width: auto");
  expect(topRuleBlock(CSS, ".stackedValue")).toContain("white-space: normal");
  expect(topRuleBlock(CSS, ".stackedValue")).toContain("overflow-wrap: anywhere");
});

test("a stacked row's help follows its value as a further definition of the same term", () => {
  render(
    <PropertyList
      layout="stacked"
      rows={[
        {
          label: "Run dir",
          value: "/tmp/evener-run",
          mono: true,
          help: "Per-PID rendezvous files the hub watches to discover live daemons.",
        },
        { label: "Spawn timeout", value: "30s" },
      ]}
    />,
  );
  expect(screen.getAllByRole("term").map((term) => term.textContent)).toEqual(["Run dir", "Spawn timeout"]);
  expect(screen.getAllByRole("definition").map((definition) => definition.textContent)).toEqual([
    "/tmp/evener-run",
    "Per-PID rendezvous files the hub watches to discover live daemons.",
    "30s",
  ]);
  const [runDir] = screen.getAllByRole("term");
  expect(runDir!.parentElement!.querySelectorAll("dd")).toHaveLength(2);
  expect(topRuleBlock(CSS, ".help")).toContain("color: var(--ink-mid)");
  expect(topRuleBlock(CSS, ".help")).toContain("font-size: var(--font-size-caption)");
});
