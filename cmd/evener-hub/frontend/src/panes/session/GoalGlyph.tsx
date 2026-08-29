export interface GoalGlyphProps {
  className?: string;
}

// Evener has no dedicated goal icon in the shared ToolIcon enum. This flag
// follows that icon family's 16px line-art grammar wherever session chrome or
// composer context needs to name a goal.
export function GoalGlyph({ className }: GoalGlyphProps) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      width={14}
      height={14}
      aria-hidden="true"
      focusable="false"
      style={{ display: "block" }}
    >
      <path
        d="M4 2 V14 M4 3 L11 5 L4 7"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}
