import type { ActivityJob } from "@evener/appwire-client";
import virtualStyles from "../../widgets/virtuallist/virtuallist.module.css";

export function jobLogOutputText(root: HTMLElement): string {
  return Array.from(root.querySelectorAll("[data-joblog-kind=output]"))
    .map((element) => element.textContent)
    .join("");
}

/** Supply the browser measurements jsdom lacks, keeping the virtual list real. */
export function installJobLogGeometry(viewportHeight = 100, rowHeight = 20): () => void {
  const names = ["offsetHeight", "clientHeight", "scrollHeight", "getBoundingClientRect", "scrollTo"] as const;
  const descriptors = names.map(
    (name) => [name, Object.getOwnPropertyDescriptor(HTMLElement.prototype, name)] as const,
  );
  Object.defineProperties(HTMLElement.prototype, {
    offsetHeight: {
      configurable: true,
      get(this: HTMLElement) {
        return this.classList.contains(virtualStyles.root ?? "") ? viewportHeight : rowHeight;
      },
    },
    clientHeight: { configurable: true, get: () => viewportHeight },
    scrollHeight: {
      configurable: true,
      get(this: HTMLElement) {
        return Number.parseFloat((this.firstElementChild as HTMLElement | null)?.style.height ?? "0") || viewportHeight;
      },
    },
    getBoundingClientRect: {
      configurable: true,
      value(this: HTMLElement) {
        const item = this.closest<HTMLElement>("[data-index]");
        const port = this.closest<HTMLElement>(`.${virtualStyles.root}`);
        const start = Number(item?.style.transform.match(/translateY\(([-\d.]+)px\)/)?.[1] ?? 0);
        const top = item ? start - (port?.scrollTop ?? 0) : 0;
        const height = item ? rowHeight : viewportHeight;
        return { x: 0, y: top, top, bottom: top + height, left: 0, right: 500, width: 500, height, toJSON() {} };
      },
    },
    scrollTo: {
      configurable: true,
      value(this: HTMLElement, options: ScrollToOptions | number) {
        const previous = this.scrollTop;
        this.scrollTop = typeof options === "number" ? options : (options.top ?? this.scrollTop);
        if (previous === this.scrollTop) return;
        queueMicrotask(() => {
          if (this.isConnected) this.dispatchEvent(new Event("scroll"));
        });
      },
    },
  });
  return () => {
    for (const [name, descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(HTMLElement.prototype, name, descriptor);
      else Reflect.deleteProperty(HTMLElement.prototype, name);
    }
  };
}

export function jobOutputMetadata(overrides: Partial<ActivityJob> = {}): ActivityJob {
  return {
    jobId: "job_x",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: false,
    hasOutput: true,
    description: "shell job",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 0,
    ...overrides,
  };
}
