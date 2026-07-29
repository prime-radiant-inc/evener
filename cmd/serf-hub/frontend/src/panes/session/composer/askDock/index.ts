// Public surface for the ask_user answering dock (wave-5 plan T4). See
// AskDock.tsx's own header for the full mount-expectations note (the
// useAskDockPending hide/inert seam and what this component deliberately
// does NOT own). Everything else in this
// directory (askDockStore, deriveAskQuestions, reconcileBatches,
// askCompose) is this feature's own implementation detail, not part of
// its public contract - a sibling stream that needs one of those directly
// should ask for it to be exported here first.
export type { AskDockProps } from "./AskDock";
export { AskDock, useAskDockPending } from "./AskDock";
