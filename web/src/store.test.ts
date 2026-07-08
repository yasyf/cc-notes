import { describe, expect, it } from "vitest";
import type { CommitPage, CommitsPage } from "./api";
import { initialState, reducer, type State } from "./store";

// The generation guard is the fix for a stale "Load older" page landing after an
// SSE refresh has reset the list: a reset bumps commits.gen, every fetch carries
// the gen it was issued under, and the reducer drops any commits response whose
// gen no longer matches. These cases drive the pure reducer directly.

function commit(sha: string, parent?: string): CommitPage {
  return {
    sha,
    parents: parent !== undefined ? [parent] : [],
    author: "ann",
    time: 100,
    summary: sha,
    branch: "main",
    tasks: [],
    events: [],
  };
}

function page(commits: CommitPage[], nextBefore: string | null): CommitsPage {
  return { commits, next_before: nextBefore, truncated: false };
}

function withCommits(overrides: Partial<State["commits"]>): State {
  return { ...initialState, commits: { ...initialState.commits, ...overrides } };
}

describe("reducer focus-commit", () => {
  it("sets and clears the one-shot focus sha", () => {
    const set = reducer(initialState, { type: "focus-commit", sha: "abc123" });
    expect(set.focusCommit).toBe("abc123");
    const cleared = reducer(set, { type: "focus-commit", sha: null });
    expect(cleared.focusCommit).toBeNull();
  });
});

describe("reducer commits generation guard", () => {
  it("bumps the generation and clears rows on a reset start", () => {
    const state = withCommits({ rows: [commit("c1")], gen: 1, loaded: true });
    const next = reducer(state, { type: "commits-load-start", reset: true, gen: 2 });
    expect(next.commits.gen).toBe(2);
    expect(next.commits.rows).toEqual([]);
    expect(next.commits.loading).toBe(true);
    expect(next.commits.loaded).toBe(false);
  });

  it("appends a current-generation page onto the existing rows", () => {
    const state = withCommits({ rows: [commit("c2")], gen: 2, loaded: true });
    const next = reducer(state, {
      type: "commits-loaded",
      page: page([commit("c1", "c0")], "c-1"),
      reset: false,
      gen: 2,
    });
    expect(next.commits.rows.map((c) => c.sha)).toEqual(["c2", "c1"]);
    expect(next.commits.nextBefore).toBe("c-1");
    expect(next.commits.gen).toBe(2);
  });

  it("drops a stale-generation append, leaving state untouched", () => {
    const state = withCommits({ rows: [commit("c2")], gen: 2, loaded: true });
    const next = reducer(state, {
      type: "commits-loaded",
      page: page([commit("c1", "c0")], "c-1"),
      reset: false,
      gen: 1,
    });
    expect(next).toBe(state);
    expect(next.commits.rows.map((c) => c.sha)).toEqual(["c2"]);
  });

  it("drops a stale-generation error, leaving state untouched", () => {
    const state = withCommits({ rows: [commit("c2")], gen: 2, loading: true, loaded: true });
    const next = reducer(state, {
      type: "commits-load-error",
      error: "boom",
      gen: 1,
    });
    expect(next).toBe(state);
    expect(next.commits.error).toBeNull();
  });

  it("drops the stale older page but applies the fresh reset across a refresh race", () => {
    // loadFirst(gen 1) populates, loadOlder(gen 1) goes in flight, an SSE refresh
    // resets to gen 2, then the stale older page (gen 1) resolves last and must be
    // dropped while the fresh reset page (gen 2) lands.
    let state = initialState;
    state = reducer(state, { type: "commits-load-start", reset: true, gen: 1 });
    state = reducer(state, {
      type: "commits-loaded",
      page: page([commit("c3"), commit("c2", "c1")], "c1"),
      reset: true,
      gen: 1,
    });
    state = reducer(state, { type: "commits-load-start", reset: false, gen: 1 });
    // SSE refresh resets under a new generation.
    state = reducer(state, { type: "commits-load-start", reset: true, gen: 2 });
    // Stale older page (issued under gen 1) resolves last — dropped.
    state = reducer(state, {
      type: "commits-loaded",
      page: page([commit("c0", "c-1")], "c-1"),
      reset: false,
      gen: 1,
    });
    expect(state.commits.rows).toEqual([]);
    // Fresh reset page (gen 2) lands.
    state = reducer(state, {
      type: "commits-loaded",
      page: page([commit("c3"), commit("c2", "c1")], "c1"),
      reset: true,
      gen: 2,
    });
    expect(state.commits.rows.map((c) => c.sha)).toEqual(["c3", "c2"]);
    expect(state.commits.gen).toBe(2);
  });
});
