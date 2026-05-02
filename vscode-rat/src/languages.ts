/**
 * languages.ts — Single registry for rat language metadata.
 *
 * Keep aliases, source extensions, VS Code language ids, tree-sitter grammars,
 * and Markdown cell snippets in one place so extension features do not drift.
 */

export type RatLang = "py" | "r" | "jl" | "js" | "sh" | "pi" | "slack";

export interface RatLanguageSpec {
  ratLang: RatLang;
  displayName: string;
  /** Fence names and user-facing aliases accepted for this language. */
  aliases: string[];
  /** Preferred fence language when inserting new cells. */
  canonicalFence?: string;
  vscodeLanguageIds: string[];
  sourceExtensions: string[];
  treeSitterWasm?: string;
  markdownSnippet?: {
    prefix: string;
    fence: string;
    label: string;
  };
  syntaxHighlight?: string;
  commentLine?: string;
}

export const RAT_LANGUAGES: readonly RatLanguageSpec[] = [
  {
    ratLang: "py",
    displayName: "Python",
    aliases: ["python", "py", "python3"],
    canonicalFence: "python",
    vscodeLanguageIds: ["python"],
    sourceExtensions: [".py", ".pyw"],
    treeSitterWasm: "tree-sitter-python.wasm",
    markdownSnippet: { prefix: "py", fence: "python", label: "Python cell" },
    syntaxHighlight: "python",
    commentLine: "#",
  },
  {
    ratLang: "r",
    displayName: "R",
    aliases: ["r", "R", "rlang"],
    canonicalFence: "r",
    vscodeLanguageIds: ["r"],
    sourceExtensions: [".r", ".R"],
    treeSitterWasm: "tree-sitter-r.wasm",
    markdownSnippet: { prefix: "r", fence: "r", label: "R cell" },
    syntaxHighlight: "r",
    commentLine: "#",
  },
  {
    ratLang: "sh",
    displayName: "Shell",
    aliases: ["bash", "sh", "shell", "zsh"],
    canonicalFence: "bash",
    vscodeLanguageIds: ["shellscript", "bash"],
    sourceExtensions: [".sh", ".bash", ".zsh"],
    treeSitterWasm: "tree-sitter-bash.wasm",
    markdownSnippet: { prefix: "sh", fence: "bash", label: "Shell cell" },
    syntaxHighlight: "bash",
    commentLine: "#",
  },
  {
    ratLang: "jl",
    displayName: "Julia",
    aliases: ["julia", "jl", "ju"],
    canonicalFence: "julia",
    vscodeLanguageIds: ["julia"],
    sourceExtensions: [".jl"],
    treeSitterWasm: "tree-sitter-julia.wasm",
    markdownSnippet: { prefix: "ju", fence: "julia", label: "Julia cell" },
    syntaxHighlight: "julia",
    commentLine: "#",
  },
  {
    ratLang: "js",
    displayName: "JavaScript",
    aliases: ["javascript", "js", "node"],
    canonicalFence: "javascript",
    vscodeLanguageIds: ["javascript"],
    sourceExtensions: [".js", ".mjs"],
    treeSitterWasm: "tree-sitter-javascript.wasm",
    markdownSnippet: { prefix: "js", fence: "javascript", label: "JavaScript cell" },
    syntaxHighlight: "javascript",
    commentLine: "//",
  },
  {
    ratLang: "pi",
    displayName: "Pi",
    aliases: ["pi"],
    canonicalFence: "pi",
    vscodeLanguageIds: [],
    sourceExtensions: [],
    markdownSnippet: { prefix: "pi", fence: "pi", label: "Pi cell" },
    syntaxHighlight: "text",
  },
  {
    ratLang: "slack",
    displayName: "Slack",
    aliases: ["slack"],
    canonicalFence: "slack",
    vscodeLanguageIds: [],
    sourceExtensions: [],
    syntaxHighlight: "text",
  },
] as const;

export const NOTEBOOK_EXTENSIONS = [".md", ".qmd", ".rmd"] as const;
export const NOTEBOOK_LANGUAGE_IDS = ["markdown", "quarto", "rmarkdown"] as const;

const aliasToRatLang = new Map<string, RatLang>();
const sourceExtensionToRatLang = new Map<string, RatLang>();
const vscodeLanguageIdToRatLang = new Map<string, RatLang>();
const specByRatLang = new Map<RatLang, RatLanguageSpec>();

for (const spec of RAT_LANGUAGES) {
  specByRatLang.set(spec.ratLang, spec);
  aliasToRatLang.set(spec.ratLang, spec.ratLang);
  for (const alias of spec.aliases) {
    aliasToRatLang.set(alias, spec.ratLang);
    aliasToRatLang.set(alias.toLowerCase(), spec.ratLang);
  }
  for (const ext of spec.sourceExtensions) {
    sourceExtensionToRatLang.set(ext, spec.ratLang);
    sourceExtensionToRatLang.set(ext.toLowerCase(), spec.ratLang);
  }
  for (const languageId of spec.vscodeLanguageIds) {
    vscodeLanguageIdToRatLang.set(languageId, spec.ratLang);
  }
}

export function ratLangForAlias(value: string | null | undefined): RatLang | null {
  if (!value) return null;
  return aliasToRatLang.get(value) ?? aliasToRatLang.get(value.toLowerCase()) ?? null;
}

export const ratLangForFence = ratLangForAlias;

export function ratLangForSourceExtension(ext: string): RatLang | null {
  return sourceExtensionToRatLang.get(ext) ?? sourceExtensionToRatLang.get(ext.toLowerCase()) ?? null;
}

export function ratLangForVscodeLanguageId(languageId: string): RatLang | null {
  return vscodeLanguageIdToRatLang.get(languageId) ?? null;
}

export function languageSpec(ratLang: string): RatLanguageSpec | undefined {
  return specByRatLang.get(ratLang as RatLang);
}

export function grammarWasmForRatLang(ratLang: string): string | null {
  return languageSpec(ratLang)?.treeSitterWasm ?? null;
}

export function canonicalFenceForRatLang(ratLang: string): string | null {
  const spec = languageSpec(ratLang);
  return spec?.canonicalFence ?? spec?.aliases[0] ?? null;
}

export function syntaxHighlightForRatLang(ratLang: string): string | null {
  return languageSpec(ratLang)?.syntaxHighlight ?? null;
}

export interface CellSnippet {
  prefix: string;
  fence: string;
  label: string;
}

export function markdownCellSnippets(): CellSnippet[] {
  return RAT_LANGUAGES
    .map((spec) => spec.markdownSnippet)
    .filter((snippet): snippet is CellSnippet => !!snippet);
}
