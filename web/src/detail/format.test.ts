import { describe, expect, it } from "vitest";
import {
  codeLanguage,
  extIcon,
  fileExt,
  formatBytes,
  isTimeField,
  scalarText,
  shortId,
  viewerKind,
} from "./format";

describe("formatBytes", () => {
  it("matches the server's 1024-base humanSize", () => {
    const cases: [number, string][] = [
      [0, "0 B"],
      [48, "48 B"],
      [1023, "1023 B"],
      [1024, "1.0 KB"],
      [1536, "1.5 KB"],
      [1048576, "1.0 MB"],
      [5 * 1024 * 1024, "5.0 MB"],
      [1024 ** 3, "1.0 GB"],
    ];
    for (const [n, want] of cases) {
      expect(formatBytes(n)).toBe(want);
    }
  });
});

describe("fileExt", () => {
  it("returns the lowercased extension, or empty for none/dotfiles", () => {
    expect(fileExt("trace.PNG")).toBe("png");
    expect(fileExt("a/b/report.tar.gz")).toBe("gz");
    expect(fileExt("Makefile")).toBe("");
    expect(fileExt(".gitignore")).toBe("");
    expect(fileExt("dir.d/file")).toBe("");
  });
});

describe("viewerKind", () => {
  it("classifies by extension and known extensionless names", () => {
    const cases: [string, string][] = [
      ["photo.png", "image"],
      ["diagram.SVG", "image"],
      ["spec.pdf", "pdf"],
      ["README.md", "markdown"],
      ["main.go", "code"],
      ["query.sql", "code"],
      ["widget.rb", "code"],
      ["notes.txt", "text"],
      ["Dockerfile", "code"],
      ["Makefile", "text"],
      [".gitignore", "text"],
      ["archive.zip", "binary"],
      ["mystery", "binary"],
    ];
    for (const [name, want] of cases) {
      expect(viewerKind(name)).toBe(want);
    }
  });
});

describe("codeLanguage", () => {
  it("maps code extensions to registered highlight.js languages, else null", () => {
    expect(codeLanguage("main.go")).toBe("go");
    expect(codeLanguage("app.tsx")).toBe("typescript");
    expect(codeLanguage("conf.toml")).toBe("ini");
    expect(codeLanguage("Dockerfile")).toBe("dockerfile");
    expect(codeLanguage("widget.rb")).toBeNull(); // code viewer, no highlighter
    expect(codeLanguage("notes.txt")).toBeNull();
  });
});

describe("isTimeField", () => {
  it("flags unix-second timestamp fields only", () => {
    expect(isTimeField("verified_at")).toBe(true);
    expect(isTimeField("closed_at")).toBe(true);
    expect(isTimeField("start_date")).toBe(true);
    expect(isTimeField("priority")).toBe(false);
    expect(isTimeField("heartbeat_lamport")).toBe(false);
    expect(isTimeField("created_at")).toBe(false); // hidden by the trail
  });
});

describe("scalarText", () => {
  it("renders null and empty string as ∅, others faithfully", () => {
    expect(scalarText(null)).toBe("∅");
    expect(scalarText("")).toBe("∅");
    expect(scalarText("open")).toBe("open");
    expect(scalarText(3)).toBe("3");
    expect(scalarText(0)).toBe("0");
    expect(scalarText(true)).toBe("true");
    expect(scalarText({ kind: "path", value: "a.go" })).toBe(
      '{"kind":"path","value":"a.go"}',
    );
  });
});

describe("shortId", () => {
  it("truncates long ids and keeps short ones", () => {
    expect(shortId("abcdef1234567890")).toBe("abcdef12");
    expect(shortId("t1")).toBe("t1");
  });
});

describe("extIcon", () => {
  it("returns a distinct non-empty glyph per viewer kind", () => {
    const glyphs = new Set([
      extIcon("a.png"),
      extIcon("a.pdf"),
      extIcon("a.md"),
      extIcon("a.go"),
      extIcon("a.txt"),
      extIcon("a.zip"),
    ]);
    expect(glyphs.size).toBe(6);
    for (const g of glyphs) expect(g.length).toBeGreaterThan(0);
  });
});
