// One source for the not-prose contract: MermaidDiagram's root carries this
// attribute, transcript prose walks skip it (EntityText's skip selector,
// AgentMarkdown's link walk), and the browser guard selects on it. Kept in its
// own module so those non-widget callers import the string alone instead of the
// whole widget.
export const MERMAID_DIAGRAM_ATTR = "data-mermaid-diagram";
