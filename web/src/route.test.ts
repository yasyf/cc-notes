import { describe, expect, it } from "vitest";
import { formatRoute, parseRoute, type Route } from "./route";

describe("parseRoute", () => {
  const cases: { name: string; hash: string; want: Route }[] = [
    { name: "empty", hash: "", want: { tab: "timeline", selection: null } },
    { name: "bare hash", hash: "#", want: { tab: "timeline", selection: null } },
    { name: "root slash", hash: "#/", want: { tab: "timeline", selection: null } },
    { name: "timeline", hash: "#/timeline", want: { tab: "timeline", selection: null } },
    { name: "commits", hash: "#/commits", want: { tab: "commits", selection: null } },
    { name: "browse", hash: "#/browse", want: { tab: "browse", selection: null } },
    {
      name: "browse with selection",
      hash: "#/browse?e=task:ab12cd",
      want: { tab: "browse", selection: { kind: "task", id: "ab12cd", title: "" } },
    },
    {
      name: "selection on timeline (tab-independent)",
      hash: "#/timeline?e=note:deadbeef",
      want: { tab: "timeline", selection: { kind: "note", id: "deadbeef", title: "" } },
    },
    {
      name: "investigation selection",
      hash: "#/timeline?e=investigation:abc1234",
      want: {
        tab: "timeline",
        selection: { kind: "investigation", id: "abc1234", title: "" },
      },
    },
    {
      name: "percent-encoded id round-trips",
      hash: "#/browse?e=task:a%2Fb",
      want: { tab: "browse", selection: { kind: "task", id: "a/b", title: "" } },
    },
    { name: "unknown tab falls back to timeline", hash: "#/nope", want: { tab: "timeline", selection: null } },
    { name: "garbage path", hash: "#garbage", want: { tab: "timeline", selection: null } },
    { name: "empty selection dropped", hash: "#/browse?e=", want: { tab: "browse", selection: null } },
    { name: "missing id dropped", hash: "#/browse?e=task:", want: { tab: "browse", selection: null } },
    { name: "missing colon dropped", hash: "#/browse?e=task", want: { tab: "browse", selection: null } },
    { name: "unknown kind dropped", hash: "#/browse?e=bogus:ab12", want: { tab: "browse", selection: null } },
    { name: "leading-colon id dropped", hash: "#/browse?e=:ab12", want: { tab: "browse", selection: null } },
    { name: "malformed percent escape dropped", hash: "#/browse?e=task:%zz", want: { tab: "browse", selection: null } },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(parseRoute(c.hash)).toEqual(c.want);
    });
  }
});

describe("formatRoute", () => {
  it("formats a bare tab", () => {
    expect(formatRoute({ tab: "browse", selection: null })).toBe("#/browse");
  });
  it("formats a tab with a selection", () => {
    expect(
      formatRoute({ tab: "browse", selection: { kind: "task", id: "ab12", title: "ignored" } }),
    ).toBe("#/browse?e=task:ab12");
  });
});

describe("round-trip formatRoute(parseRoute(x))", () => {
  const canonical = [
    "#/timeline",
    "#/commits",
    "#/browse",
    "#/browse?e=task:ab12cd",
    "#/timeline?e=note:deadbeef",
    "#/timeline?e=investigation:abc1234",
  ];
  for (const hash of canonical) {
    it(hash, () => {
      expect(formatRoute(parseRoute(hash))).toBe(hash);
    });
  }
});
