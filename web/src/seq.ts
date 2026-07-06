// Latest-wins request sequencer: next() hands out a monotonically increasing
// token per request, and isLatest(token) reports whether that token is still the
// most recent one issued. A caller guards an async result with the token it was
// issued under so a superseded (out-of-order, older) response is dropped.

export interface Sequencer {
  next: () => number;
  isLatest: (token: number) => boolean;
}

// createSequencer builds a fresh Sequencer whose first token is 1.
export function createSequencer(): Sequencer {
  let latest = 0;
  return {
    next: () => (latest += 1),
    isLatest: (token) => token === latest,
  };
}
