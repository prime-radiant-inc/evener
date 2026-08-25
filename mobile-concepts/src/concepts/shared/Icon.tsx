import type { SVGProps } from "react";

export type IconName =
  | "sessions"
  | "search"
  | "new"
  | "settings"
  | "back"
  | "switch"
  | "lab"
  | "warning"
  | "running"
  | "complete"
  | "waiting"
  | "failed"
  | "tool"
  | "work"
  | "voice"
  | "send"
  | "stop"
  | "close"
  | "chevron";

type IconAccessibility =
  | { decorative: true; label?: never }
  | { decorative: false; label: string };

export type IconProps = IconAccessibility &
  Omit<SVGProps<SVGSVGElement>, "aria-label" | "aria-hidden" | "role"> & {
    name: IconName;
  };

const paths: Readonly<Record<IconName, string>> = {
  sessions: "M4 5h16v5H4zM4 14h16v5H4z",
  search: "M10.5 4a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13m5 11 4.5 4.5",
  new: "M12 4v16M4 12h16",
  settings:
    "M12 3v3m0 12v3M3 12h3m12 0h3M5.6 5.6l2.1 2.1m8.6 8.6 2.1 2.1m0-12.8-2.1 2.1m-8.6 8.6-2.1 2.1M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6",
  back: "M15 5l-7 7 7 7",
  switch: "M7 7h11l-3-3m3 3-3 3M17 17H6l3 3m-3-3 3-3",
  lab: "M9 3h6m-5 0v6l-5 9a2 2 0 0 0 2 3h10a2 2 0 0 0 2-3l-5-9V3M8 15h8",
  warning: "M12 3 2.5 20h19zM12 9v5m0 3v.5",
  running:
    "M12 3v4m0 10v4M3 12h4m10 0h4M5.6 5.6l2.8 2.8m7.2 7.2 2.8 2.8m0-12.8-2.8 2.8m-7.2 7.2-2.8 2.8",
  complete: "M4 12l5 5L20 6",
  waiting:
    "M6 3h12M6 21h12M8 3c0 5 3 6 4 9-1 3-4 4-4 9m8-18c0 5-3 6-4 9 1 3 4 4 4 9",
  failed: "M5 5l14 14M19 5 5 19",
  tool: "M14 5a4 4 0 0 0-5 5L3 16l5 5 6-6a4 4 0 0 0 5-5l-3 3-3-3 3-3z",
  work: "M3 7h18v13H3zM8 7V4h8v3M3 12h18",
  voice:
    "M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3M6 11v1a6 6 0 0 0 12 0v-1M12 18v3M9 21h6",
  send: "M3 11.5 21 3l-8.5 18-2-7.5zM10.5 13.5 21 3",
  stop: "M6 6h12v12H6z",
  close: "M5 5l14 14m0-14L5 19",
  chevron: "M9 5l7 7-7 7",
};

export function Icon({ name, decorative, label, ...svgProps }: IconProps) {
  return (
    <svg
      {...svgProps}
      aria-hidden={decorative ? "true" : undefined}
      aria-label={decorative ? undefined : label}
      fill="none"
      focusable="false"
      role={decorative ? undefined : "img"}
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="2"
      viewBox="0 0 24 24"
    >
      <path d={paths[name]} />
    </svg>
  );
}
