// App state: a React context over useReducer holding the loaded graph, the repo
// header, the current entity selection, and the live-stream connection status.
// The provider is a pure container — App wires the fetch and the SSE stream and
// dispatches into it.

import {
  createContext,
  createElement,
  useContext,
  useReducer,
  type Dispatch,
  type ReactNode,
} from "react";
import type { CommitPage, CommitsPage, Graph, RepoInfo, StateResponse } from "./api";
import type { Connection } from "./stream";

// Tab is the active top-level view. The hash route (route.ts) is its source of
// truth, so it lives in the store rather than local component state.
export type Tab = "timeline" | "commits" | "browse";

export interface Selection {
  kind: string;
  id: string;
  title: string;
}

// CommitsState holds the DAG tab's flattened pages: every fetched page's commits
// concatenated newest -> oldest, the cursor for the next (older) page, whether
// the underlying walk hit the DAG horizon, and in-flight/error status. `loaded`
// gates the tab's lazy first fetch. `gen` is the client page-reset generation
// (distinct from State.gen, the server refs generation): a reset bumps it, and a
// commits response carrying a superseded generation is dropped, so a stale
// "Load older" page can never append onto a list a refresh has since reset.
export interface CommitsState {
  rows: CommitPage[];
  nextBefore: string | null;
  truncated: boolean;
  loading: boolean;
  loaded: boolean;
  error: string | null;
  gen: number;
}

export const initialCommits: CommitsState = {
  rows: [],
  nextBefore: null,
  truncated: false,
  loading: false,
  loaded: false,
  error: null,
  gen: 0,
};

// EntitiesState holds the full folded snapshot of every live entity, powering
// the Browse tab's table/kanban and the header's global jump-to search. `gen` is
// a latest-wins guard mirroring CommitsState: each fetch carries the generation
// it was issued under and the reducer drops any response a later fetch has
// superseded, so an in-flight refetch can never overwrite fresher data.
export interface EntitiesState {
  data: StateResponse | null;
  loading: boolean;
  loaded: boolean;
  error: string | null;
  gen: number;
}

export const initialEntities: EntitiesState = {
  data: null,
  loading: false,
  loaded: false,
  error: null,
  gen: 0,
};

export interface State {
  repo: RepoInfo | null;
  graph: Graph | null;
  selection: Selection | null;
  connection: Connection;
  loading: boolean;
  error: string | null;
  gen: number;
  tab: Tab;
  commits: CommitsState;
  entities: EntitiesState;
  // focusCommit names a commit the Commits tab should scroll to and flash, set
  // when a commit chip in the detail panel is clicked. CommitGraph clears it once
  // the row has flashed, so it is a one-shot request rather than a persistent
  // selection.
  focusCommit: string | null;
}

export type Action =
  | { type: "load-start" }
  | { type: "loaded"; repo: RepoInfo; graph: Graph }
  | { type: "load-error"; error: string }
  | { type: "select"; selection: Selection | null }
  | { type: "connection"; connection: Connection }
  | { type: "gen"; gen: number }
  | { type: "tab"; tab: Tab }
  | { type: "focus-commit"; sha: string | null }
  | { type: "commits-load-start"; reset: boolean; gen: number }
  | { type: "commits-loaded"; page: CommitsPage; reset: boolean; gen: number }
  | { type: "commits-load-error"; error: string; gen: number }
  | { type: "entities-load-start"; gen: number }
  | { type: "entities-loaded"; data: StateResponse; gen: number }
  | { type: "entities-load-error"; error: string; gen: number };

export const initialState: State = {
  repo: null,
  graph: null,
  selection: null,
  connection: "connecting",
  loading: true,
  error: null,
  gen: 0,
  tab: "timeline",
  commits: initialCommits,
  entities: initialEntities,
  focusCommit: null,
};

export function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "load-start":
      return { ...state, loading: true };
    case "loaded":
      return {
        ...state,
        repo: action.repo,
        graph: action.graph,
        loading: false,
        error: null,
      };
    case "load-error":
      return { ...state, loading: false, error: action.error };
    case "select":
      return { ...state, selection: action.selection };
    case "connection":
      return { ...state, connection: action.connection };
    case "gen":
      return { ...state, gen: action.gen };
    case "tab":
      return { ...state, tab: action.tab };
    case "focus-commit":
      return { ...state, focusCommit: action.sha };
    case "commits-load-start":
      return {
        ...state,
        commits: action.reset
          ? { ...initialCommits, gen: action.gen, loading: true }
          : { ...state.commits, loading: true },
      };
    case "commits-loaded":
      if (action.gen !== state.commits.gen) return state;
      return {
        ...state,
        commits: {
          rows: action.reset
            ? action.page.commits
            : [...state.commits.rows, ...action.page.commits],
          nextBefore: action.page.next_before,
          truncated: action.page.truncated,
          loading: false,
          loaded: true,
          error: null,
          gen: state.commits.gen,
        },
      };
    case "commits-load-error":
      if (action.gen !== state.commits.gen) return state;
      return {
        ...state,
        commits: { ...state.commits, loading: false, error: action.error },
      };
    case "entities-load-start":
      return {
        ...state,
        entities: { ...state.entities, loading: true, gen: action.gen },
      };
    case "entities-loaded":
      if (action.gen !== state.entities.gen) return state;
      return {
        ...state,
        entities: {
          data: action.data,
          loading: false,
          loaded: true,
          error: null,
          gen: state.entities.gen,
        },
      };
    case "entities-load-error":
      if (action.gen !== state.entities.gen) return state;
      return {
        ...state,
        entities: { ...state.entities, loading: false, error: action.error },
      };
  }
}

const StateContext = createContext<State | null>(null);
const DispatchContext = createContext<Dispatch<Action> | null>(null);

export function StoreProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(reducer, initialState);
  return createElement(
    StateContext.Provider,
    { value: state },
    createElement(DispatchContext.Provider, { value: dispatch }, children),
  );
}

export function useStore(): State {
  const ctx = useContext(StateContext);
  if (ctx === null) throw new Error("useStore must be used within StoreProvider");
  return ctx;
}

export function useDispatch(): Dispatch<Action> {
  const ctx = useContext(DispatchContext);
  if (ctx === null)
    throw new Error("useDispatch must be used within StoreProvider");
  return ctx;
}
