// Commit-page loader: dispatches the fetch/append/reset lifecycle for the DAG
// tab. loadFirst replaces the page stack (the lazy first fetch and the
// stream-driven refresh); loadOlder appends the next page so assignColumns can
// re-run over the concatenated list and keep rails continuous across the paging
// boundary.

import { useCallback, useRef } from "react";
import { fetchCommits } from "../api";
import { useDispatch, useStore } from "../store";

export interface CommitsLoader {
  loadFirst: () => void;
  loadOlder: (before: string) => void;
}

export function useCommitsLoader(): CommitsLoader {
  const dispatch = useDispatch();
  const { commits } = useStore();
  // Mirror the committed generation so the stable callbacks below read the
  // latest value at call time — loadFirst reset()s under gen+1, loadOlder
  // append()s under the current gen, and the reducer drops any response whose
  // generation a later reset has already superseded.
  const genRef = useRef(commits.gen);
  genRef.current = commits.gen;

  const loadFirst = useCallback(() => {
    const gen = genRef.current + 1;
    dispatch({ type: "commits-load-start", reset: true, gen });
    fetchCommits()
      .then((page) => dispatch({ type: "commits-loaded", page, reset: true, gen }))
      .catch((err: unknown) =>
        dispatch({ type: "commits-load-error", error: String(err), gen }),
      );
  }, [dispatch]);

  const loadOlder = useCallback(
    (before: string) => {
      const gen = genRef.current;
      dispatch({ type: "commits-load-start", reset: false, gen });
      fetchCommits(before)
        .then((page) => dispatch({ type: "commits-loaded", page, reset: false, gen }))
        .catch((err: unknown) =>
          dispatch({ type: "commits-load-error", error: String(err), gen }),
        );
    },
    [dispatch],
  );

  return { loadFirst, loadOlder };
}
