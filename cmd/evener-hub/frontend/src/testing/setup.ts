// Vitest hooks are imported rather than global, so Testing Library cannot
// install its automatic act-environment setup. Direct React act calls in
// projection helpers need the same environment as Testing Library's wrapper.
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
