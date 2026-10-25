// Hold only work scheduled directly by the widget, not React's own microtasks.
export function holdVirtualListMeasurementMicrotasks() {
  const native = globalThis.queueMicrotask;
  const pending: VoidFunction[] = [];
  globalThis.queueMicrotask = (callback) => {
    if (new Error().stack?.split("\n")[2]?.includes("/widgets/virtuallist/index.tsx")) pending.push(callback);
    else native(callback);
  };
  return {
    pending,
    restore() {
      globalThis.queueMicrotask = native;
    },
  };
}
