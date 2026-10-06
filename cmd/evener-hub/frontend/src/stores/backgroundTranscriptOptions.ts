export interface BackgroundTranscriptOptions {
  residentBytes: number;
  pendingBytes: number;
  diskWorkers: 1;
  networkHydrations: number;
}

export const DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS: BackgroundTranscriptOptions = {
  residentBytes: 16 * 1024 * 1024,
  pendingBytes: 4 * 1024 * 1024,
  diskWorkers: 1,
  networkHydrations: 2,
};

let installed: BackgroundTranscriptOptions | undefined;

export function backgroundTranscriptOptions(): Readonly<BackgroundTranscriptOptions> {
  return installed ?? DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS;
}

export function installBackgroundTranscriptOptionsForTests(options: Partial<BackgroundTranscriptOptions>): () => void {
  const next = { ...backgroundTranscriptOptions(), ...options };
  if (
    !Number.isSafeInteger(next.residentBytes) ||
    next.residentBytes < 0 ||
    !Number.isSafeInteger(next.pendingBytes) ||
    next.pendingBytes <= 0 ||
    next.diskWorkers !== 1 ||
    !Number.isSafeInteger(next.networkHydrations) ||
    next.networkHydrations <= 0
  ) {
    throw new RangeError("invalid background transcript limits");
  }
  const previous = installed;
  installed = next;
  return () => {
    installed = previous;
  };
}
