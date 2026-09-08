// Hook-only seam for Composer.tsx: reads the ask-dock pending flag without
// pulling AskDock.tsx (the answering surface + AskQuestionCard) into the
// composer's initial chunk. Session.tsx keeps importing the full barrel -
// it mounts AskDock itself - so this changes only what the composer pays
// for, not what any other surface loads.
//
// The predicate lives in askDock/askDockStore.ts, next to the store it
// selects from; this module re-exports it so the composer's chunk stays lean
// without carrying its own verbatim copy.
export { useAskDockPending } from "./askDock/askDockStore";
