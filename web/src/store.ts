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
import type { CommitPage, CommitsPage, Graph, RepoInfo } from "./api";
import type { Connection } from "./stream";

export interface Selection {
  kind: string;
  id: string;
  title: string;
}

// CommitsState holds the DAG tab's flattened pages: every fetched page's commits
// concatenated newest -> oldest, the cursor for the next (older) page, whether
// the underlying walk hit the DAG horizon, and in-flight/error status. `loaded`
// gates the tab's lazy first fetch.
export interface CommitsState {
  rows: CommitPage[];
  nextBefore: string | null;
  truncated: boolean;
  loading: boolean;
  loaded: boolean;
  error: string | null;
}

export const initialCommits: CommitsState = {
  rows: [],
  nextBefore: null,
  truncated: false,
  loading: false,
  loaded: false,
  error: null,
};

export interface State {
  repo: RepoInfo | null;
  graph: Graph | null;
  selection: Selection | null;
  connection: Connection;
  loading: boolean;
  error: string | null;
  gen: number;
  commits: CommitsState;
}

export type Action =
  | { type: "load-start" }
  | { type: "loaded"; repo: RepoInfo; graph: Graph }
  | { type: "load-error"; error: string }
  | { type: "select"; selection: Selection | null }
  | { type: "connection"; connection: Connection }
  | { type: "gen"; gen: number }
  | { type: "commits-load-start"; reset: boolean }
  | { type: "commits-loaded"; page: CommitsPage; reset: boolean }
  | { type: "commits-load-error"; error: string };

export const initialState: State = {
  repo: null,
  graph: null,
  selection: null,
  connection: "connecting",
  loading: true,
  error: null,
  gen: 0,
  commits: initialCommits,
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
    case "commits-load-start":
      return {
        ...state,
        commits: action.reset
          ? { ...initialCommits, loading: true }
          : { ...state.commits, loading: true },
      };
    case "commits-loaded":
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
        },
      };
    case "commits-load-error":
      return {
        ...state,
        commits: { ...state.commits, loading: false, error: action.error },
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
