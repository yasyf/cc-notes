// Modal viewer for one attachment, portalled to <body>. Escape or a backdrop
// click closes it. The viewer is chosen by the file name's extension: images and
// PDFs stream straight from /api/blob behind a one-byte Range probe; markdown,
// code, and text are fetched and rendered (highlighted, with a line-number
// gutter, for code) up to a 1 MB cap; anything else is download-only. Every
// viewer surfaces a blob failure as the server's JSON error verbatim — an
// unfetched object's message already says how to fix it. The footer always
// offers the raw download.

import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { blobURL, type Attachment } from "../api";
import { codeLanguage, formatBytes, viewerKind, type ViewerKind } from "./format";
import { highlightHTML } from "./highlight";
import { Markdown } from "./Markdown";

const TEXT_CAP = 1_000_000;

export function AttachmentModal({
  attachment,
  onClose,
}: {
  attachment: Attachment;
  onClose: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const kind = viewerKind(attachment.name);
  const href = blobURL(attachment.oid, attachment.name);

  return createPortal(
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <div
        className="modal-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={attachment.name}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="modal-head">
          <span className="modal-title" title={attachment.name}>
            {attachment.name}
          </span>
          <span className="modal-size">{formatBytes(attachment.size)}</span>
          <button type="button" className="modal-close" aria-label="Close" onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal-body">
          <Viewer kind={kind} attachment={attachment} href={href} />
        </div>
        <footer className="modal-foot">
          <a className="modal-download" href={href} download={attachment.name}>
            Download / open raw
          </a>
        </footer>
      </div>
    </div>,
    document.body,
  );
}

function Viewer({
  kind,
  attachment,
  href,
}: {
  kind: ViewerKind;
  attachment: Attachment;
  href: string;
}) {
  if (kind === "image") {
    return (
      <BlobGate href={href}>
        <img className="modal-image" src={href} alt={attachment.name} />
      </BlobGate>
    );
  }
  if (kind === "pdf") {
    return (
      <BlobGate href={href}>
        <embed className="modal-pdf" src={href} type="application/pdf" />
      </BlobGate>
    );
  }
  if (kind === "binary") {
    return <p className="modal-note">No inline preview for this file type — use the download link below.</p>;
  }
  if (attachment.size > TEXT_CAP) {
    return (
      <p className="modal-note">
        File is {formatBytes(attachment.size)} — too large to preview inline. Use the download
        link below.
      </p>
    );
  }
  return <TextViewer kind={kind} attachment={attachment} href={href} />;
}

type Fetched =
  | { state: "loading" }
  | { state: "error"; message: string }
  | { state: "ready"; text: string };

// BlobGate probes the blob with a one-byte Range request before letting a
// browser-streamed viewer (img, embed) render, so a missing object shows the
// server's error message instead of a broken-media icon.
function BlobGate({ href, children }: { href: string; children: ReactNode }) {
  const [f, setF] = useState<Fetched>({ state: "loading" });

  useEffect(() => {
    let cancelled = false;
    setF({ state: "loading" });
    fetch(href, { headers: { Range: "bytes=0-0" } })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res));
        if (!cancelled) setF({ state: "ready", text: "" });
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setF({ state: "error", message: err instanceof Error ? err.message : String(err) });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [href]);

  if (f.state === "loading") return <p className="modal-note">Loading…</p>;
  if (f.state === "error") return <p className="modal-note modal-error">{f.message}</p>;
  return <>{children}</>;
}

function TextViewer({
  kind,
  attachment,
  href,
}: {
  kind: ViewerKind;
  attachment: Attachment;
  href: string;
}) {
  const [f, setF] = useState<Fetched>({ state: "loading" });

  useEffect(() => {
    let cancelled = false;
    setF({ state: "loading" });
    fetch(href, { headers: { Accept: "*/*" } })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res));
        return res.text();
      })
      .then((text) => {
        if (!cancelled) setF({ state: "ready", text });
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setF({ state: "error", message: err instanceof Error ? err.message : String(err) });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [href]);

  if (f.state === "loading") return <p className="modal-note">Loading…</p>;
  if (f.state === "error") return <p className="modal-note modal-error">{f.message}</p>;
  if (kind === "markdown") {
    return (
      <div className="modal-md">
        <Markdown>{f.text}</Markdown>
      </div>
    );
  }
  return <CodeView text={f.text} language={codeLanguage(attachment.name)} />;
}

function CodeView({ text, language }: { text: string; language: string | null }) {
  const html = highlightHTML(text, language);
  const lines = text.split("\n");
  const count = lines.length > 1 && lines[lines.length - 1] === "" ? lines.length - 1 : lines.length;
  const nums = Array.from({ length: count }, (_, i) => i + 1).join("\n");
  return (
    <div className="code-view">
      <pre className="code-nums" aria-hidden="true">
        {nums}
      </pre>
      <pre className="code-body">
        <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
    </div>
  );
}

// errorMessage extracts the server's {"error": msg} body, falling back to the
// HTTP status line when the body is not that shape.
async function errorMessage(res: Response): Promise<string> {
  try {
    const body: unknown = await res.json();
    if (body !== null && typeof body === "object" && "error" in body) {
      const msg = (body as { error: unknown }).error;
      if (typeof msg === "string") return msg;
    }
  } catch {
    // body was not JSON; fall through to the status line.
  }
  return `${res.status} ${res.statusText}`;
}
