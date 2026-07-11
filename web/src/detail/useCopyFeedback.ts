// useCopyFeedback drives the copy-then-flash pattern shared by the chip
// components: copy() writes to the clipboard and flips `copied` true for one
// second, clearing any pending timer on unmount or a fresh copy.

import { useEffect, useRef, useState } from "react";

const FLASH_MS = 1000;

export interface CopyFeedback {
  copied: boolean;
  copy: (text: string) => void;
}

export function useCopyFeedback(): CopyFeedback {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(
    () => () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
    },
    [],
  );

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(true);
    if (timer.current !== undefined) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), FLASH_MS);
  };

  return { copied, copy };
}
