// Supplies browser geometry to real transcript components in jsdom, never restoration policy.
export interface TranscriptTestGeometry {
  width: number;
  scrollbarWidth?: number;
  viewportHeight: number;
  rowHeights: readonly number[];
  entryBoxes?: Readonly<Record<string, { top: number; height: number }>>;
}

export function installTranscriptGeometry(
  geometryFor: (element: HTMLElement) => TranscriptTestGeometry,
  findPort?: (element: HTMLElement) => HTMLElement | undefined,
): { notify(include?: (target: HTMLElement) => boolean): void; observedTargets(): Element[]; restore(): void } {
  const prototype = HTMLElement.prototype;
  const saved = new Map<string, PropertyDescriptor | undefined>();
  const nativeRect = prototype.getBoundingClientRect;
  const nativeRects = prototype.getClientRects;
  const nativeObserver = Object.getOwnPropertyDescriptor(globalThis, "ResizeObserver");
  const offsets = new WeakMap<HTMLElement, number>();
  let active = true;
  const portFor = (element: HTMLElement): HTMLElement | undefined => {
    if (findPort) return findPort(element);
    const child = element.closest('[data-testid="transcript-virtual-list"]')?.firstElementChild;
    return child instanceof HTMLElement ? child : undefined;
  };
  const rowFor = (element: HTMLElement) => element.closest<HTMLElement>("[data-index]");
  const rowHeight = (element: HTMLElement, port: HTMLElement) => {
    const index = rowFor(element)?.dataset.index;
    return index === undefined ? geometryFor(port).viewportHeight : (geometryFor(port).rowHeights[Number(index)] ?? 0);
  };
  const rectFor = (element: HTMLElement): DOMRect => {
    const port = portFor(element);
    if (!port) return nativeRect.call(element);
    const geometry = geometryFor(port);
    if (element === port) return new DOMRect(0, 0, geometry.width, geometry.viewportHeight);
    const contentWidth = geometry.width - (geometry.scrollbarWidth ?? 0);
    if (element === port.firstElementChild)
      return new DOMRect(0, -port.scrollTop, contentWidth, Number.parseFloat(element.style.height) || 0);
    const closed = element.closest("details:not([open])");
    const summary = closed?.querySelector(":scope > summary");
    if (closed && element !== closed && !summary?.contains(element)) return new DOMRect();
    const row = rowFor(element);
    if (!row) return nativeRect.call(element);
    const box = Object.entries(geometry.entryBoxes ?? {}).find(([selector]) => element.matches(selector))?.[1];
    const translation = Number.parseFloat(row.style.transform.match(/^translateY\(([-\d.]+)px\)$/)?.[1] ?? "0");
    return new DOMRect(
      0,
      translation + (box?.top ?? 0) - port.scrollTop,
      contentWidth,
      box?.height ?? rowHeight(element, port),
    );
  };
  const replace = (key: string, descriptor: PropertyDescriptor) => {
    saved.set(key, Object.getOwnPropertyDescriptor(prototype, key));
    Object.defineProperty(prototype, key, { configurable: true, ...descriptor });
  };
  for (const key of ["offsetWidth", "clientWidth"])
    replace(key, {
      get(this: HTMLElement) {
        const port = portFor(this);
        if (!port) return 0;
        const geometry = geometryFor(port);
        return geometry.width - (key === "offsetWidth" && this === port ? 0 : (geometry.scrollbarWidth ?? 0));
      },
    });
  for (const key of ["offsetHeight", "clientHeight"])
    replace(key, {
      get(this: HTMLElement) {
        const port = portFor(this);
        return port ? rowHeight(this, port) : 0;
      },
    });
  replace("scrollHeight", {
    get(this: HTMLElement) {
      const port = portFor(this);
      return this === port
        ? Number.parseFloat(port.firstElementChild?.getAttribute("style")?.match(/height:\s*([-\d.]+)px/)?.[1] ?? "0")
        : 0;
    },
  });
  replace("scrollTop", {
    get(this: HTMLElement) {
      return Math.max(0, Math.min(offsets.get(this) ?? 0, Math.max(0, this.scrollHeight - this.clientHeight)));
    },
    set(this: HTMLElement, value: number) {
      const previous = this.scrollTop;
      offsets.set(this, Math.max(0, Math.min(value, Math.max(0, this.scrollHeight - this.clientHeight))));
      if (this.scrollTop !== previous)
        queueMicrotask(() => {
          if (active && this.isConnected) this.dispatchEvent(new Event("scroll"));
        });
    },
  });
  replace("scrollTo", {
    writable: true,
    value(this: HTMLElement, optionsOrX: ScrollToOptions | number = {}, y?: number) {
      this.scrollTop = typeof optionsOrX === "number" ? (y ?? this.scrollTop) : (optionsOrX.top ?? this.scrollTop);
    },
  });
  replace("getBoundingClientRect", {
    value(this: HTMLElement) {
      return rectFor(this);
    },
  });
  replace("getClientRects", {
    value(this: HTMLElement) {
      if (!portFor(this)) return nativeRects.call(this);
      const rect = rectFor(this);
      const rects = rect.width > 0 && rect.height > 0 ? [rect] : [];
      return Object.assign(rects, { item: (index: number) => rects[index] ?? null });
    },
  });
  const observers = new Set<GeometryResizeObserver>();
  class GeometryResizeObserver implements ResizeObserver {
    readonly targets = new Set<Element>();
    constructor(readonly callback: ResizeObserverCallback) {
      observers.add(this);
    }
    observe(target: Element) {
      this.targets.add(target);
    }
    unobserve(target: Element) {
      this.targets.delete(target);
    }
    disconnect() {
      this.targets.clear();
      observers.delete(this);
    }
  }
  Object.defineProperty(globalThis, "ResizeObserver", {
    configurable: true,
    writable: true,
    value: GeometryResizeObserver,
  });
  return {
    observedTargets() {
      return [...observers].flatMap((observer) => [...observer.targets]);
    },
    notify(include = () => true) {
      for (const observer of [...observers]) {
        const entries: ResizeObserverEntry[] = [...observer.targets].flatMap((target) => {
          if (!(target instanceof HTMLElement) || !target.isConnected || !include(target)) return [];
          const rect = rectFor(target);
          const size = [{ inlineSize: rect.width, blockSize: rect.height }];
          return [
            { target, contentRect: rect, borderBoxSize: size, contentBoxSize: size, devicePixelContentBoxSize: size },
          ];
        });
        if (entries.length > 0) observer.callback(entries, observer);
      }
    },
    restore() {
      active = false;
      for (const observer of [...observers]) observer.disconnect();
      for (const [key, descriptor] of saved) {
        if (descriptor) Object.defineProperty(prototype, key, descriptor);
        else Reflect.deleteProperty(prototype, key);
      }
      if (nativeObserver) Object.defineProperty(globalThis, "ResizeObserver", nativeObserver);
      else Reflect.deleteProperty(globalThis, "ResizeObserver");
    },
  };
}
