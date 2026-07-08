// highlight.js core with a curated set of ~15 common languages registered once,
// shared by the markdown code renderer and the attachment code viewer. Output is
// hljs-escaped HTML (safe to inject via dangerouslySetInnerHTML); an unregistered
// language falls through to plain escaped text.

import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import markdown from "highlight.js/lib/languages/markdown";
import python from "highlight.js/lib/languages/python";
import rust from "highlight.js/lib/languages/rust";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("css", css);
hljs.registerLanguage("diff", diff);
hljs.registerLanguage("dockerfile", dockerfile);
hljs.registerLanguage("go", go);
hljs.registerLanguage("ini", ini);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("json", json);
hljs.registerLanguage("markdown", markdown);
hljs.registerLanguage("python", python);
hljs.registerLanguage("rust", rust);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("xml", xml);
hljs.registerLanguage("yaml", yaml);

hljs.registerAliases(["tsx"], { languageName: "typescript" });
hljs.registerAliases(["shell", "zsh"], { languageName: "bash" });
hljs.registerAliases(["html"], { languageName: "xml" });
hljs.registerAliases(["toml"], { languageName: "ini" });

// hasLanguage reports whether a language name (or alias) is registered.
export function hasLanguage(language: string | null): boolean {
  return language !== null && hljs.getLanguage(language) !== undefined;
}

// highlightHTML returns hljs-highlighted HTML for the code when the language is
// registered, or plain escaped text otherwise.
export function highlightHTML(code: string, language: string | null): string {
  if (hasLanguage(language)) {
    return hljs.highlight(code, { language: language as string, ignoreIllegals: true }).value;
  }
  return escapeHTML(code);
}

function escapeHTML(s: string): string {
  return s.replace(/[&<>]/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"));
}
