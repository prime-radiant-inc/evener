// jsdom has no intersection engine. Route visibility through the observer that
// watches the actual pending-page skeleton, leaving unrelated observers alone.
export class ActivityBoundaryObserver {
  static observers: ActivityBoundaryObserver[] = [];
  private target?: Element;
  constructor(private readonly callback: IntersectionObserverCallback) {
    ActivityBoundaryObserver.observers.push(this);
  }
  observe(target: Element): void {
    this.target = target;
  }
  disconnect(): void {}
  emit(visible: boolean): void {
    this.callback(
      [{ target: this.target, isIntersecting: visible } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
  static latest(): ActivityBoundaryObserver {
    const observer = ActivityBoundaryObserver.observers.at(-1);
    if (!observer) throw new Error("No activity observer");
    return observer;
  }
  static boundary(): ActivityBoundaryObserver {
    let observer: ActivityBoundaryObserver | undefined;
    for (let index = ActivityBoundaryObserver.observers.length - 1; index >= 0; index -= 1) {
      const candidate = ActivityBoundaryObserver.observers[index];
      if (candidate?.target?.querySelector('[data-testid="skeleton-line"]')) {
        observer = candidate;
        break;
      }
    }
    if (!observer) throw new Error("No observed activity page boundary");
    return observer;
  }
  static intersect(): void {
    ActivityBoundaryObserver.boundary().emit(true);
  }
  static reset(): void {
    ActivityBoundaryObserver.observers = [];
  }
}
