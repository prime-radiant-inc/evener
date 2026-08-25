import { Component, type ErrorInfo, type ReactNode } from "react";

export interface RecoveryBoundaryProps {
  children: ReactNode;
  resetPrototype(): void;
}

interface RecoveryBoundaryState {
  error: Error | null;
}

export class RecoveryBoundary extends Component<
  RecoveryBoundaryProps,
  RecoveryBoundaryState
> {
  state: RecoveryBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): RecoveryBoundaryState {
    return { error };
  }

  componentDidCatch(_error: Error, _info: ErrorInfo) {
    // This local prototype has no telemetry or remote recovery transport.
  }

  private readonly recover = () => {
    this.props.resetPrototype();
    this.setState({ error: null });
  };

  render() {
    if (this.state.error !== null) {
      return (
        <section className="recovery" role="alert">
          <p className="recovery__eyebrow">Local recovery</p>
          <h2>This concept could not be rendered.</h2>
          <p>Reset the deterministic prototype and choose a concept again.</p>
          <button type="button" onClick={this.recover}>
            Reset prototype
          </button>
        </section>
      );
    }
    return this.props.children;
  }
}
