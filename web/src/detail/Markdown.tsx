// The single markdown renderer used across the detail panel — note/doc bodies,
// task/sprint/project descriptions, log entries, comments, and text attachment
// previews. GitHub-flavoured markdown via remark-gfm; raw HTML is dropped by
// react-markdown (no rehype-raw), which is the sanitization story. Fenced code
// blocks are highlighted through the shared highlight.js registry.

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { hasLanguage, highlightHTML } from "./highlight";

const components: Components = {
  // react-markdown wraps a block in <pre><code>; unwrap the <pre> so the code
  // renderer below emits the single styled <pre> itself.
  pre: ({ children }) => <>{children}</>,
  code(props) {
    const { className, children } = props;
    const text = String(children ?? "");
    const match = /language-([\w-]+)/.exec(className ?? "");
    const block = match !== null || text.includes("\n");
    if (!block) return <code className="md-code-inline">{children}</code>;

    const language = match?.[1] ?? null;
    const code = text.replace(/\n$/, "");
    const html = highlightHTML(code, language);
    return (
      <pre className="md-pre">
        <code
          className={hasLanguage(language) ? "md-code hljs" : "md-code"}
          dangerouslySetInnerHTML={{ __html: html }}
        />
      </pre>
    );
  },
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noreferrer noopener">
      {children}
    </a>
  ),
};

export function Markdown({ children }: { children: string }) {
  return (
    <div className="md">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {children}
      </ReactMarkdown>
    </div>
  );
}
