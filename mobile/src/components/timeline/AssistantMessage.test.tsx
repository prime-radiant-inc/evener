import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AssistantMessage } from "./AssistantMessage";
import { PlainTextBlock } from "./PlainTextBlock";

afterEach(() => {
  cleanup();
});

describe("AssistantMessage — sanitized HTML injection", () => {
  it("renders sanitized assistant markdown as HTML", () => {
    render(<AssistantMessage source="**bold** text" />);
    expect(screen.getByText("bold").tagName).toBe("STRONG");
  });

  it("strips script tags from assistant markdown", () => {
    render(<AssistantMessage source={"<script>alert(1)</script>\n\nhello"} />);
    expect(screen.queryByText("alert(1)")).toBeNull();
    expect(screen.getByText("hello")).toBeDefined();
  });

  it("does not render images", () => {
    render(<AssistantMessage source="![alt](https://example.com/x.png)" />);
    expect(document.querySelector("img")).toBeNull();
  });
});

describe("AssistantMessage — external link handling", () => {
  it("displays the link destination and invokes onExternalLink on tap", () => {
    const onExternalLink = vi.fn();
    render(
      <AssistantMessage
        source="[docs](https://example.com)"
        onExternalLink={onExternalLink}
      />,
    );
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe("https://example.com");
    fireEvent.click(link);
    expect(onExternalLink).toHaveBeenCalledWith("https://example.com/");
  });

  it("prevents default navigation when onExternalLink is provided", () => {
    const onExternalLink = vi.fn();
    render(
      <AssistantMessage
        source="[docs](https://example.com)"
        onExternalLink={onExternalLink}
      />,
    );
    const link = screen.getByRole("link");
    const event = new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      composed: true,
    });
    fireEvent(link, event);
    expect(event.defaultPrevented).toBe(true);
  });

  it("does not invoke onExternalLink when tapping non-link content", () => {
    const onExternalLink = vi.fn();
    render(
      <AssistantMessage source="plain text" onExternalLink={onExternalLink} />,
    );
    fireEvent.click(screen.getByText("plain text"));
    expect(onExternalLink).not.toHaveBeenCalled();
  });

  it("does not throw when onExternalLink is absent", () => {
    render(<AssistantMessage source="[docs](https://example.com)" />);
    expect(() => fireEvent.click(screen.getByRole("link"))).not.toThrow();
  });

  it("rejects javascript: links so onExternalLink never receives them", () => {
    const onExternalLink = vi.fn();
    render(
      <AssistantMessage
        source="[bad](javascript:alert(1))"
        onExternalLink={onExternalLink}
      />,
    );
    const link = screen.queryByRole("link");
    // The javascript: protocol is stripped, so either no link or a safe link.
    if (link) {
      fireEvent.click(link);
      expect(onExternalLink).not.toHaveBeenCalledWith(
        expect.stringContaining("javascript:"),
      );
    }
  });
});

describe("AssistantMessage — streaming prop", () => {
  it("sets data-streaming attribute when streaming", () => {
    render(<AssistantMessage source="text" streaming />);
    const container = document.querySelector(".evener-assistant-message");
    expect(container?.getAttribute("data-streaming")).toBe("true");
  });

  it("omits data-streaming when not streaming", () => {
    render(<AssistantMessage source="text" />);
    const container = document.querySelector(".evener-assistant-message");
    expect(container?.getAttribute("data-streaming")).toBeNull();
  });
});

describe("PlainTextBlock — escaped plain text", () => {
  it("renders text as escaped text nodes, not HTML", () => {
    render(<PlainTextBlock text="<script>alert(1)</script>" />);
    // React escapes the string; no script element is created.
    expect(document.querySelector("script")).toBeNull();
    expect(screen.getByText("<script>alert(1)</script>")).toBeDefined();
  });

  it("renders special characters literally", () => {
    render(<PlainTextBlock text="a < b > c & d" />);
    expect(screen.getByText("a < b > c & d")).toBeDefined();
  });

  it("applies optional className", () => {
    render(<PlainTextBlock text="x" className="custom" />);
    expect(
      document.querySelector(".evener-plaintext-block.custom"),
    ).not.toBeNull();
  });

  it("does not use dangerouslySetInnerHTML", () => {
    render(<PlainTextBlock text="<b>bold</b>" />);
    // The literal text is shown, not rendered as bold.
    expect(screen.queryByText("bold")).toBeNull();
    expect(screen.getByText("<b>bold</b>")).toBeDefined();
  });
});
