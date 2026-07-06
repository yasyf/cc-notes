// Commit-page loader: dispatches the fetch/append/reset lifecycle for the DAG
// tab. loadFirst replaces the page stack (the lazy first fetch and the
// stream-driven refresh); loadOlder appends the next page so assignColumns can
// re-run over the concatenated list and keep rails continuous across the paging
// boundary.

import { useCallback } from "react";
import { fetchCommits } from "../api";
import { useDispatch } from "../store";

export interface CommitsLoader {
  loadFirst: () => void;
  loadOlder: (before: string) => void;
}

export function useCommitsLoader(): CommitsLoader {
  const dispatch = useDispatch();

  const loadFirst = useCallback(() => {
    dispatch({ type: "commits-load-start", reset: true });
    fetchCommits()
      .then((page) => dispatch({ type: "commits-loaded", page, reset: true }))
      .catch((err: unknown) => dispatch({ type: "commits-load-error", error: String(err) }));
  }, [dispatch]);

  const loadOlder = useCallback(
    (before: string) => {
      dispatch({ type: "commits-load-start", reset: false });
      fetchCommits(before)
        .then((page) => dispatch({ type: "commits-loaded", page, reset: false }))
        .catch((err: unknown) => dispatch({ type: "commits-load-error", error: String(err) }));
    },
    [dispatch],
  );

  return { loadFirst, loadOlder };
}
