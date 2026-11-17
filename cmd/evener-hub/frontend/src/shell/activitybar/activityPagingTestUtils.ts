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
    const observer = ActivityBoundaryObserver.boundary();
    observer.callback(
      [{ target: observer.target, isIntersecting: true } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );
  }
  static reset(): void {
    ActivityBoundaryObserver.observers = [];
  }
}
