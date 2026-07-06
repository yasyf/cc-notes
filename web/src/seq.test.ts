import { describe, expect, it } from "vitest";
import { createSequencer } from "./seq";

// The sequencer is the latest-wins guard behind App's load(): only the most
// recent token is "latest", so an older request that resolves after a newer one
// is ignored. Each case pins the exact isLatest verdicts against a hand-traced
// issue order.

describe("createSequencer", () => {
  it("hands out monotonically increasing tokens starting at 1", () => {
    const seq = createSequencer();
    expect(seq.next()).toBe(1);
    expect(seq.next()).toBe(2);
    expect(seq.next()).toBe(3);
  });

  it("treats only the most recently issued token as latest", () => {
    const seq = createSequencer();
    const first = seq.next();
    expect(seq.isLatest(first)).toBe(true);
    const second = seq.next();
    expect(seq.isLatest(first)).toBe(false);
    expect(seq.isLatest(second)).toBe(true);
  });

  it("ignores an out-of-order older resolution after a newer request", () => {
    // Two loads in flight: token1 issued first, token2 issued after. token1's
    // response arrives last (out of order) and must be dropped; token2 wins.
    const seq = createSequencer();
    const token1 = seq.next();
    const token2 = seq.next();

    // token2 resolves first — it is still latest, so it dispatches.
    expect(seq.isLatest(token2)).toBe(true);
    // token1 resolves after — superseded, so it is dropped.
    expect(seq.isLatest(token1)).toBe(false);
  });
});
