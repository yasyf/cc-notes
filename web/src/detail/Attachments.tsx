// Attachment chips and the modal they open. A chip shows an extension-derived
// icon glyph, the file name, and a human-readable size; clicking it opens the
// AttachmentModal. AttachmentProvider owns the open-modal state so any chip in
// the snapshot or the trail can trigger it through context.

import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import type { Attachment } from "../api";
import { AttachmentModal } from "./AttachmentModal";
import { extIcon, formatBytes } from "./format";

type OpenFn = (a: Attachment) => void;

const OpenCtx = createContext<OpenFn>(() => {});

export function AttachmentProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<Attachment | null>(null);
  const openFn = useCallback<OpenFn>((a) => setOpen(a), []);
  return (
    <OpenCtx.Provider value={openFn}>
      {children}
      {open !== null && <AttachmentModal attachment={open} onClose={() => setOpen(null)} />}
    </OpenCtx.Provider>
  );
}

export function AttachmentChip({ attachment }: { attachment: Attachment }) {
  const open = useContext(OpenCtx);
  return (
    <button
      type="button"
      className="attach-chip"
      title={`${attachment.name} (${formatBytes(attachment.size)})`}
      onClick={() => open(attachment)}
    >
      <span className="attach-icon" aria-hidden="true">
        {extIcon(attachment.name)}
      </span>
      <span className="attach-name">{attachment.name}</span>
      <span className="attach-size">{formatBytes(attachment.size)}</span>
    </button>
  );
}

export function Attachments({ items }: { items: Attachment[] }) {
  return (
    <div className="attach-row">
      {items.map((a) => (
        <AttachmentChip key={`${a.oid}-${a.name}`} attachment={a} />
      ))}
    </div>
  );
}
