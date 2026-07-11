import { describe, expect, it } from "vitest";
import type { RunbookSnapshot, StateResponse } from "../api";
import { buildIndex } from "./index";

function emptyState(over: Partial<StateResponse> = {}): StateResponse {
  return {
    notes: [],
    docs: [],
    logs: [],
    tasks: [],
    sprints: [],
    projects: [],
    runbooks: [],
    ...over,
  };
}

function runbook(over: Partial<RunbookSnapshot> = {}): RunbookSnapshot {
  return {
    id: "rb1",
    title: "Deploy service",
    description: "the release procedure",
    status: "active",
    steps: [
      { id: "s1", text: "build the image", command: "make image", position: "a0" },
      { id: "s2", text: "roll out", command: "", position: "a1" },
    ],
    runs: [],
    labels: ["ops", "release"],
    comments: [{ author: "ann", ts: 5, body: "watch the canary" }],
    author: "ann",
    created_at: 1,
    updated_at: 42,
    archived_at: 0,
    head: "",
    ...over,
  };
}

describe("runbookRow", () => {
  it("projects a runbook into a Row with its status, labels, and a searchable body", () => {
    const rows = buildIndex(emptyState({ runbooks: [runbook()] }));
    expect(rows).toHaveLength(1);
    const row = rows[0];
    if (row === undefined) throw new Error("expected a row");
    expect(row.kind).toBe("runbook");
    expect(row.id).toBe("rb1");
    expect(row.title).toBe("Deploy service");
    expect(row.status).toBe("active");
    expect(row.tags).toEqual(["ops", "release"]);
    expect(row.priority).toBeNull();
    expect(row.verifiable).toBe(false);
    expect(row.updated).toBe(42);
    // bodyLower folds description, every step's text and command, labels,
    // comment bodies, and id.
    for (const needle of [
      "the release procedure",
      "build the image",
      "make image",
      "roll out",
      "ops",
      "release",
      "watch the canary",
      "rb1",
    ]) {
      expect(row.bodyLower).toContain(needle);
    }
  });

  it("places runbooks after logs and before sprints in buildIndex order", () => {
    const state = emptyState({
      logs: [
        {
          id: "l1",
          title: "log",
          entries: [],
          tags: [],
          anchors: [],
          author: "",
          created_at: 0,
          updated_at: 0,
          deleted: false,
          head: "",
        },
      ],
      runbooks: [runbook()],
      sprints: [
        {
          id: "sp1",
          project: "",
          title: "sprint",
          description: "",
          status: "active",
          start_date: 0,
          end_date: 0,
          labels: [],
          commits: [],
          comments: [],
          author: "",
          created_at: 0,
          updated_at: 0,
          started_at: 0,
          closed_at: 0,
          head: "",
        },
      ],
    });
    expect(buildIndex(state).map((r) => r.kind)).toEqual(["log", "runbook", "sprint"]);
  });
});
