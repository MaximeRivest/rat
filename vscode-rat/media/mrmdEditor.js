/* global acquireVsCodeApi, mrmd */
(function () {
  "use strict";

  const vscode = acquireVsCodeApi();
  const root = document.getElementById("editor");
  const status = document.getElementById("status");
  const openTextButton = document.getElementById("openText");
  const saveButton = document.getElementById("save");

  let editor = null;
  let unwatchTheme = null;
  let applyingHostUpdate = false;
  let editTimer = 0;
  let lastSentText = "";
  let nextId = 1;

  const pendingRuns = new Map();
  const pendingRpcs = new Map();

  const supportedLanguages = [
    "python", "py", "python3",
    "r", "rlang",
    "bash", "sh", "shell", "zsh",
    "julia", "jl", "ju",
    "javascript", "js", "node",
    "pi", "slack",
  ];

  const supportedSet = new Set(supportedLanguages);

  const HOST_THEME_OVERRIDES = {
    "--widget-font-mono": "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
    "--widget-font-sans": "var(--vscode-font-family, system-ui, sans-serif)",
    "--editor-font-family": "var(--vscode-editor-font-family, var(--vscode-font-family, system-ui, sans-serif))",
    "--editor-font-size": "var(--vscode-editor-font-size, 14px)",
    "--editor-background": "var(--vscode-editor-background)",
    "--editor-foreground": "var(--vscode-editor-foreground)",
    "--editor-line-number": "var(--vscode-editorLineNumber-foreground, var(--vscode-descriptionForeground))",
    "--editor-line-number-active": "var(--vscode-editorLineNumber-activeForeground, var(--vscode-editor-foreground))",
    "--editor-selection": "var(--vscode-editor-selectionBackground)",
    "--editor-selection-match": "var(--vscode-editor-selectionHighlightBackground, var(--vscode-editor-selectionBackground))",
    "--editor-cursor": "var(--vscode-editorCursor-foreground)",
    "--editor-active-line": "var(--vscode-editor-lineHighlightBackground, transparent)",
    "--editor-gutter": "var(--vscode-editorGutter-background, var(--vscode-editor-background))",
    "--editor-matching-bracket": "var(--vscode-editorBracketMatch-background, transparent)",

    "--widget-text": "var(--vscode-editor-foreground)",
    "--widget-text-muted": "var(--vscode-descriptionForeground)",
    "--widget-text-accent": "var(--vscode-textLink-foreground)",
    "--widget-surface": "var(--vscode-editorWidget-background, color-mix(in srgb, var(--vscode-editor-foreground) 6%, transparent))",
    "--widget-surface-hover": "var(--vscode-list-hoverBackground, color-mix(in srgb, var(--vscode-editor-foreground) 10%, transparent))",
    "--widget-surface-elevated": "var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)))",
    "--widget-surface-inset": "var(--vscode-input-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent))",
    "--widget-border": "var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 18%, transparent)))",
    "--widget-border-accent": "var(--vscode-focusBorder, var(--vscode-textLink-foreground))",
    "--widget-border-focus": "var(--vscode-focusBorder, var(--vscode-textLink-foreground))",
    "--widget-success": "var(--vscode-testing-iconPassed, var(--vscode-charts-green, #22c55e))",
    "--widget-warning": "var(--vscode-editorWarning-foreground, var(--vscode-charts-yellow, #f59e0b))",
    "--widget-error": "var(--vscode-editorError-foreground, var(--vscode-charts-red, #ef4444))",
    "--widget-info": "var(--vscode-editorInfo-foreground, var(--vscode-charts-blue, #3b82f6))",

    "--mrmd-bg": "var(--vscode-editor-background)",
    "--mrmd-fg": "var(--vscode-editor-foreground)",
    "--mrmd-fg-muted": "var(--vscode-descriptionForeground)",
    "--mrmd-border": "var(--vscode-editorWidget-border, var(--vscode-panel-border, var(--widget-border)))",
    "--mrmd-panel-bg": "var(--vscode-sideBar-background, var(--vscode-editor-background))",
    "--mrmd-popup-bg": "var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)))",
    "--mrmd-hover-bg": "var(--vscode-list-hoverBackground, var(--widget-surface-hover))",
    "--mrmd-active-bg": "var(--vscode-list-activeSelectionBackground, var(--widget-surface-hover))",
    "--mrmd-selection-bg": "var(--vscode-editor-selectionBackground)",
    "--mrmd-accent": "var(--vscode-focusBorder, var(--vscode-textLink-foreground))",

    "--syntax-keyword": "var(--vscode-symbolIcon-keywordForeground, var(--vscode-charts-purple, var(--vscode-editor-foreground)))",
    "--syntax-control": "var(--vscode-symbolIcon-keywordForeground, var(--vscode-charts-purple, var(--vscode-editor-foreground)))",
    "--syntax-string": "var(--vscode-symbolIcon-stringForeground, var(--vscode-charts-green, var(--vscode-editor-foreground)))",
    "--syntax-number": "var(--vscode-symbolIcon-numberForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)))",
    "--syntax-comment": "var(--vscode-editorCodeLens-foreground, var(--vscode-descriptionForeground))",
    "--syntax-function": "var(--vscode-symbolIcon-functionForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
    "--syntax-variable": "var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground))",
    "--syntax-variable-special": "var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground))",
    "--syntax-property": "var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground))",
    "--syntax-operator": "var(--vscode-symbolIcon-operatorForeground, var(--vscode-editor-foreground))",
    "--syntax-punctuation": "var(--vscode-editor-foreground)",
    "--syntax-type": "var(--vscode-symbolIcon-classForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
    "--syntax-class": "var(--vscode-symbolIcon-classForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
    "--syntax-constant": "var(--vscode-symbolIcon-constantForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)))",
    "--syntax-parameter": "var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground))",
    "--syntax-tag": "var(--vscode-symbolIcon-structForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
    "--syntax-attribute": "var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground))",
    "--syntax-heading": "var(--vscode-editor-foreground)",
    "--syntax-link": "var(--vscode-textLink-foreground)",
    "--syntax-code": "var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground))",
    "--syntax-code-background": "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent))",
    "--syntax-meta": "var(--vscode-descriptionForeground)",

    "--md-heading-color": "var(--vscode-editor-foreground)",
    "--md-link-color": "var(--vscode-textLink-foreground)",
    "--md-code-background": "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent))",
    "--md-code-color": "var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground))",
    "--md-blockquote-border": "var(--vscode-textBlockQuote-border, var(--vscode-focusBorder))",
    "--md-blockquote-color": "var(--vscode-descriptionForeground)",
    "--md-marker-color": "var(--vscode-descriptionForeground)",
    "--md-list-marker-color": "var(--vscode-descriptionForeground)",
    "--md-hr-color": "var(--vscode-panel-border, var(--widget-border))",
  };

  function setStatus(text) {
    if (status) status.textContent = text || "";
  }

  function isDarkTheme() {
    const cls = document.body.classList;
    return cls.contains("vscode-dark") ||
      (cls.contains("vscode-high-contrast") && !cls.contains("vscode-high-contrast-light"));
  }

  function hostThemeName() {
    return isDarkTheme() ? "vscode-host-dark" : "vscode-host-light";
  }

  function registerHostThemes() {
    const widgets = (window.mrmd && window.mrmd.widgets) || window.mrmd;
    if (!widgets || typeof widgets.createTheme !== "function" || typeof widgets.registerTheme !== "function") {
      return;
    }

    widgets.registerTheme(widgets.createTheme({
      name: "vscode-host-light",
      base: "plain-light",
      description: "VS Code light theme bridge",
      overrides: HOST_THEME_OVERRIDES,
    }));
    widgets.registerTheme(widgets.createTheme({
      name: "vscode-host-dark",
      base: "plain-dark",
      description: "VS Code dark theme bridge",
      overrides: HOST_THEME_OVERRIDES,
    }));
  }

  function createHostDocumentTemplate() {
    return {
      name: "VS Code",
      version: 1,
      editor: { applyDocumentStyles: true },
      page: {
        background: "var(--vscode-editor-background)",
        maxWidth: "",
      },
      body: {
        color: "var(--vscode-editor-foreground)",
        fontFamily: "var(--vscode-editor-font-family, var(--vscode-font-family, system-ui, sans-serif))",
        fontSize: "var(--vscode-editor-font-size, 14px)",
        lineHeight: "1.6",
      },
      heading: {
        color: "var(--vscode-editor-foreground)",
      },
      link: {
        color: "var(--vscode-textLink-foreground)",
        underline: true,
      },
      blockquote: {
        borderLeftColor: "var(--vscode-textBlockQuote-border, var(--vscode-focusBorder))",
        background: "var(--vscode-textBlockQuote-background, transparent)",
        color: "var(--vscode-descriptionForeground)",
      },
      code: {
        inline: {
          fontFamily: "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
          background: "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent))",
          color: "var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground))",
        },
        block: {
          fontFamily: "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
          background: "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent))",
          color: "var(--vscode-editor-foreground)",
          borderColor: "var(--vscode-editorWidget-border, var(--vscode-panel-border))",
          borderRadius: "4px",
        },
        cell: {
          headerBackground: "var(--vscode-editorWidget-background, var(--vscode-editor-background))",
          headerColor: "var(--vscode-descriptionForeground)",
          headerBorderColor: "var(--vscode-editorWidget-border, var(--vscode-panel-border))",
          outputBackground: "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent))",
          outputColor: "var(--vscode-editor-foreground)",
          outputBorderColor: "var(--vscode-editorWidget-border, var(--vscode-panel-border))",
          outputFontFamily: "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
        },
      },
      table: {
        borderColor: "var(--vscode-editorWidget-border, var(--vscode-panel-border))",
        headerBackground: "var(--vscode-editorWidget-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent))",
        headerColor: "var(--vscode-editor-foreground)",
        color: "var(--vscode-editor-foreground)",
      },
      hr: {
        color: "var(--vscode-panel-border, var(--vscode-editorWidget-border))",
      },
    };
  }

  function installHostThemeStyle() {
    if (document.getElementById("rat-vscode-theme-style")) return;

    const style = document.createElement("style");
    style.id = "rat-vscode-theme-style";
    style.textContent = `
#editor, .mrmd-root {
  --editor-background: var(--vscode-editor-background);
  --editor-foreground: var(--vscode-editor-foreground);
  --editor-cursor: var(--vscode-editorCursor-foreground);
  --editor-selection: var(--vscode-editor-selectionBackground);
  --editor-active-line: var(--vscode-editor-lineHighlightBackground, transparent);
  --widget-font-mono: var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace);
  --widget-font-sans: var(--vscode-font-family, system-ui, sans-serif);
  --widget-text: var(--vscode-editor-foreground);
  --widget-text-muted: var(--vscode-descriptionForeground);
  --widget-text-accent: var(--vscode-textLink-foreground);
  --widget-border: var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 18%, transparent)));
  --widget-border-accent: var(--vscode-focusBorder, var(--vscode-textLink-foreground));
  --widget-surface: var(--vscode-editorWidget-background, color-mix(in srgb, var(--vscode-editor-foreground) 6%, transparent));
  --widget-surface-hover: var(--vscode-list-hoverBackground, color-mix(in srgb, var(--vscode-editor-foreground) 10%, transparent));
  --widget-surface-elevated: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)));
  --widget-surface-inset: var(--vscode-input-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent));
  --mrmd-bg: var(--vscode-editor-background);
  --mrmd-fg: var(--vscode-editor-foreground);
  --mrmd-fg-muted: var(--vscode-descriptionForeground);
  --mrmd-border: var(--vscode-editorWidget-border, var(--vscode-panel-border, var(--widget-border)));
  --mrmd-panel-bg: var(--vscode-sideBar-background, var(--vscode-editor-background));
  --mrmd-popup-bg: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)));
  --mrmd-hover-bg: var(--vscode-list-hoverBackground, var(--widget-surface-hover));
  --mrmd-active-bg: var(--vscode-list-activeSelectionBackground, var(--widget-surface-hover));
  --mrmd-selection-bg: var(--vscode-editor-selectionBackground);
  --mrmd-accent: var(--vscode-focusBorder, var(--vscode-textLink-foreground));
  --md-heading-color: var(--vscode-editor-foreground);
  --md-link-color: var(--vscode-textLink-foreground);
  --md-code-background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent));
  --md-code-color: var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground));
  --md-blockquote-border: var(--vscode-textBlockQuote-border, var(--vscode-focusBorder));
  --md-blockquote-color: var(--vscode-descriptionForeground);
  --md-marker-color: var(--vscode-descriptionForeground);
  --md-list-marker-color: var(--vscode-descriptionForeground);
  --md-hr-color: var(--vscode-panel-border, var(--widget-border));
}

#editor .cm-editor,
#editor .cm-scroller {
  background: var(--vscode-editor-background) !important;
  color: var(--vscode-editor-foreground) !important;
}

#editor .cm-content,
#editor .cm-line {
  color: var(--vscode-editor-foreground);
}

#editor .cm-cursor,
#editor .cm-dropCursor {
  border-left-color: var(--vscode-editorCursor-foreground) !important;
}

#editor .cm-selectionBackground,
#editor .cm-content ::selection {
  background: var(--vscode-editor-selectionBackground) !important;
}

#editor .cm-activeLine {
  background: var(--vscode-editor-lineHighlightBackground, transparent) !important;
}

#editor .cm-tooltip,
#editor .cm-panel {
  background: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background))) !important;
  color: var(--vscode-editor-foreground) !important;
  border-color: var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent)) !important;
}

#editor .cm-frontmatter,
#editor .cm-frontmatter-card,
#editor .cm-frontmatter-abstract,
#editor .cm-frontmatter-keyword,
#editor .cm-output,
#editor .cm-output-widget,
#editor .cm-cell-output,
#editor .mrmd-output {
  background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent));
  color: var(--vscode-editor-foreground);
  border-color: var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent));
}
`;
    document.head.appendChild(style);
  }

  function syncHostTheme() {
    registerHostThemes();
    if (!editor) return;
    const nextTheme = hostThemeName();
    if (editor.getTheme && editor.getTheme() !== nextTheme) {
      editor.setTheme(nextTheme);
    }
    if (editor.setDocumentTemplate) {
      editor.setDocumentTemplate(createHostDocumentTemplate());
    }
  }

  function watchHostTheme() {
    const observer = new MutationObserver(syncHostTheme);
    observer.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }

  function nextRequestId() {
    const id = nextId;
    nextId += 1;
    return id;
  }

  function rpc(type, payload, timeoutMs) {
    const id = nextRequestId();
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        if (!pendingRpcs.has(id)) return;
        pendingRpcs.delete(id);
        reject(new Error(type + " timed out"));
      }, timeoutMs || 30_000);

      pendingRpcs.set(id, { resolve, reject, timeout });
      vscode.postMessage(Object.assign({ type, id }, payload || {}));
    });
  }

  function resolveRpc(message, ok) {
    const pending = pendingRpcs.get(message.id);
    if (!pending) return;
    clearTimeout(pending.timeout);
    pendingRpcs.delete(message.id);

    if (ok) {
      pending.resolve(message.result);
    } else {
      pending.reject(new Error(message.error || "Request failed"));
    }
  }

  function scheduleEdit(text) {
    if (text === lastSentText) return;
    lastSentText = text;

    if (editTimer) clearTimeout(editTimer);
    editTimer = setTimeout(() => {
      editTimer = 0;
      vscode.postMessage({ type: "edit", text });
    }, 150);
  }

  function isSupported(language) {
    return supportedSet.has(String(language || "").toLowerCase());
  }

  function createAssetResolver(docDirWebviewUri) {
    const base = String(docDirWebviewUri || "").endsWith("/")
      ? String(docDirWebviewUri)
      : String(docDirWebviewUri || "") + "/";

    return function resolveAsset(url) {
      const raw = String(url || "");
      if (!raw) return raw;
      if (/^(?:https?:|data:|blob:|vscode-resource:|vscode-webview-resource:|file:)/i.test(raw)) {
        return raw;
      }
      try {
        return new URL(raw, base).toString();
      } catch {
        return raw;
      }
    };
  }

  function parseCompletionLine(line) {
    const raw = String(line || "").trimEnd();
    if (!raw || raw.trim() === "No completions.") return null;

    const kinds = new Set([
      "attribute", "class", "constant", "directory", "file", "folder",
      "function", "instance", "keyword", "method", "module", "package",
      "param", "parameter", "property", "statement", "type", "value",
      "variable",
    ]);

    const match =
      raw.match(/^(.*?)\t+([A-Za-z][\w-]*)$/) ||
      raw.match(/^(.*?)\s{2,}([A-Za-z][\w-]*)$/) ||
      raw.match(/^(.*\S)\s+([A-Za-z][\w-]*)$/);

    if (match) {
      const maybeKind = match[2].toLowerCase();
      if (kinds.has(maybeKind)) {
        const label = match[1].trimEnd();
        return label ? { label, kind: maybeKind } : null;
      }
    }

    return { label: raw.trim(), kind: "variable" };
  }

  function completionReplaceStart(code, cursor, insertText) {
    let start = cursor;
    while (start > 0 && /[^\s|&;(){}\[\]<>\'",]/.test(code[start - 1])) start -= 1;

    const prefix = code.slice(start, cursor);
    if (prefix.includes(".") && !insertText.startsWith(prefix)) {
      return start + prefix.lastIndexOf(".") + 1;
    }
    return start;
  }

  function wordAt(code, cursor) {
    const text = String(code || "");
    let start = Math.max(0, Math.min(cursor, text.length));
    let end = start;
    while (start > 0 && /[\w.]/.test(text[start - 1])) start -= 1;
    while (end < text.length && /[\w.]/.test(text[end])) end += 1;
    return text.slice(start, end).replace(/^\.+|\.+$/g, "") || null;
  }

  function splitRatAssets(text) {
    const assets = [];
    const kept = [];

    for (const line of String(text || "").split("\n")) {
      const match = line.match(/^__RAT_PLOT__:(.+)$/);
      if (match) {
        assets.push(match[1].trim());
      } else {
        kept.push(line);
      }
    }

    return { text: kept.join("\n"), assets };
  }

  function guessMime(url) {
    const lower = String(url || "").toLowerCase();
    if (lower.endsWith(".svg")) return "image/svg+xml";
    if (lower.endsWith(".jpg") || lower.endsWith(".jpeg")) return "image/jpeg";
    if (lower.endsWith(".gif")) return "image/gif";
    if (lower.endsWith(".webp")) return "image/webp";
    return "image/png";
  }

  function cancelPendingRun(execId) {
    for (const [id, run] of pendingRuns) {
      if (execId && run.execId !== execId) continue;
      vscode.postMessage({ type: "ratCancel", id });
    }
  }

  function wireCancellationBridge() {
    if (!editor || !editor.execution || editor.execution.__ratCancelBridge) return;
    editor.execution.__ratCancelBridge = true;

    const originalCancel = editor.execution.cancel.bind(editor.execution);
    editor.execution.cancel = function (index, mrpClient) {
      const execId = editor.execution.cellExecIds && editor.execution.cellExecIds.get(index);
      cancelPendingRun(execId);
      return originalCancel(index, mrpClient);
    };

    const originalCancelAll = editor.execution.cancelAll.bind(editor.execution);
    editor.execution.cancelAll = function (mrpClient) {
      cancelPendingRun(null);
      return originalCancelAll(mrpClient);
    };
  }

  function completeRun(message) {
    const run = pendingRuns.get(message.id);
    if (!run) return;
    pendingRuns.delete(message.id);

    const parsed = splitRatAssets(message.stdout || "");

    if (parsed.assets.length && typeof run.onAsset === "function") {
      for (const assetUrl of parsed.assets) {
        run.onAsset({
          url: assetUrl,
          mimeType: guessMime(assetUrl),
          assetType: "image",
        }, "image");
      }
    }

    const stdout = parsed.text;
    let delta;
    if (stdout.startsWith(run.accumulated)) {
      delta = stdout.slice(run.accumulated.length);
    } else if (!run.accumulated) {
      delta = stdout;
    } else {
      delta = "\n" + stdout;
    }

    try {
      run.onChunk(delta, stdout, true);
      run.resolve({
        success: !!message.success,
        stdout,
        stderr: message.stderr || "",
        error: message.error || null,
      });
    } catch (error) {
      run.reject(error);
    }
  }

  function createRatRuntime() {
    const lspProvider = {
      languages: supportedLanguages,

      async complete(code, cursor, language) {
        if (!isSupported(language)) {
          return { matches: [], cursorStart: cursor, cursorEnd: cursor, source: "runtime" };
        }

        try {
          const result = await rpc("ratComplete", { code, cursor, language }, 10_000);
          const parsed = (result && result.items ? result.items : [])
            .map(parseCompletionLine)
            .filter(Boolean);

          if (!parsed.length) {
            return { matches: [], cursorStart: cursor, cursorEnd: cursor, source: "runtime" };
          }

          let cursorStart = cursor;
          const matches = parsed.map((item) => {
            let insertText = item.label;
            if (insertText.endsWith("(")) insertText = insertText.slice(0, -1);
            cursorStart = Math.min(cursorStart, completionReplaceStart(code, cursor, insertText));
            return {
              label: item.label,
              insertText,
              kind: item.kind || "variable",
              detail: item.kind ? "rat · " + item.kind : "rat",
            };
          });

          return { matches, cursorStart, cursorEnd: cursor, source: "runtime" };
        } catch {
          return { matches: [], cursorStart: cursor, cursorEnd: cursor, source: "runtime" };
        }
      },

      async hover(code, cursor, language) {
        const at = wordAt(code, cursor);
        if (!at || !isSupported(language)) return null;

        try {
          const result = await rpc("ratInspect", { at, language }, 10_000);
          if (!result || !result.text) return null;
          return {
            found: true,
            name: at,
            documentation: result.text,
          };
        } catch {
          return null;
        }
      },

      async inspect(code, cursor, language) {
        const at = wordAt(code, cursor);
        if (!at || !isSupported(language)) return null;

        try {
          const result = await rpc("ratInspect", { at, language }, 10_000);
          if (!result || !result.text) return null;
          return {
            found: true,
            name: at,
            documentation: result.text,
            sourceCode: result.text,
          };
        } catch {
          return null;
        }
      },

      async listVariables() {
        try {
          const result = await rpc("ratInspect", { at: null, language: "python" }, 10_000);
          if (!result || !result.text) return [];
          return result.text
            .split("\n")
            .filter(Boolean)
            .map((line) => ({ name: line.trim(), type: "", value: "" }));
        } catch {
          return [];
        }
      },

      async getVariable(name) {
        try {
          const result = await rpc("ratInspect", { at: name, language: "python" }, 10_000);
          return { name, type: "", value: result && result.text ? result.text : "" };
        } catch {
          return { name, type: "unknown", value: "?", expandable: false };
        }
      },

      async isComplete() {
        return { status: "unknown" };
      },

      async format(code) {
        return { formatted: code, changed: false };
      },
    };

    return {
      supports(language) {
        return isSupported(language);
      },

      async execute(code, language) {
        return this.executeStreaming(code, language, function () {}, null, {});
      },

      executeStreaming(code, language, onChunk, onStdinRequest, options) {
        if (!isSupported(language)) {
          const message = "No rat runtime for language: " + language;
          onChunk(message + "\n", message + "\n", true);
          return Promise.resolve({
            success: false,
            stdout: "",
            stderr: message,
            error: { message },
          });
        }

        const id = nextRequestId();
        setStatus("Running " + language + " cell…");

        return new Promise((resolve, reject) => {
          pendingRuns.set(id, {
            execId: options && options.execId,
            onChunk,
            onAsset: options && options.onAsset,
            resolve: (result) => {
              setStatus(result.success ? "Done" : "Finished with errors");
              resolve(result);
            },
            reject: (error) => {
              setStatus("Run failed");
              reject(error);
            },
            accumulated: "",
          });

          vscode.postMessage({ type: "ratRun", id, code, language });
        });
      },

      getLSPProvider() {
        return lspProvider;
      },
    };
  }

  function createEditor(message) {
    if (!root) return;
    if (!window.mrmd || typeof window.mrmd.create !== "function") {
      root.textContent = "mrmd-editor failed to load.";
      return;
    }

    const ratRuntime = createRatRuntime();
    installHostThemeStyle();
    registerHostThemes();

    editor = window.mrmd.create(root, {
      doc: message.text || "",
      javascript: false,
      runtimes: { rat: ratRuntime },
      placeholder: "Write Markdown…",
      theme: hostThemeName(),
      documentTemplate: createHostDocumentTemplate(),
      documentStylePreview: true,
      themingMode: "hosted",
      projectRoot: message.projectRoot || null,
      documentPath: message.documentPath || null,
      userName: "VS Code",
    });

    if (
      message.docDirWebviewUri &&
      window.mrmd.codemirror &&
      window.mrmd.codemirror.StateEffect &&
      window.mrmd.markdown &&
      window.mrmd.markdown.assetResolverFacet
    ) {
      editor.view.dispatch({
        effects: window.mrmd.codemirror.StateEffect.appendConfig.of(
          window.mrmd.markdown.assetResolverFacet.of(createAssetResolver(message.docDirWebviewUri))
        ),
      });
    }

    installHostThemeStyle();
    if (unwatchTheme) unwatchTheme();
    unwatchTheme = watchHostTheme();
    syncHostTheme();

    if (editor.execution) {
      wireCancellationBridge();

      editor.execution.assetHandler = function (asset) {
        return rpc("ratAsset", {
          url: asset.url,
          mimeType: asset.mimeType,
          assetType: asset.assetType || "image",
        }, 30_000);
      };
    }

    editor.onChange((content) => {
      if (applyingHostUpdate) return;
      scheduleEdit(content);
    });

    editor.onSave(() => {
      vscode.postMessage({ type: "save" });
    });

    lastSentText = message.text || "";
    setStatus("Ready");
    editor.focus();
  }

  window.addEventListener("message", (event) => {
    const message = event.data || {};

    switch (message.type) {
      case "init":
        createEditor(message);
        break;

      case "setContent":
        if (!editor) return;
        if (message.text === editor.getContent()) return;
        applyingHostUpdate = true;
        editor.setContent(message.text || "");
        lastSentText = message.text || "";
        setTimeout(() => { applyingHostUpdate = false; }, 0);
        break;

      case "ratOutput": {
        const run = pendingRuns.get(message.id);
        if (!run) return;
        const chunk = String(message.chunk || "");
        run.accumulated += chunk;
        run.onChunk(chunk, run.accumulated, false);
        break;
      }

      case "ratDone":
        completeRun(message);
        break;

      case "ratError": {
        const run = pendingRuns.get(message.id);
        if (!run) return;
        pendingRuns.delete(message.id);
        run.reject(new Error(message.error || "rat execution failed"));
        break;
      }

      case "rpcResult":
        resolveRpc(message, true);
        break;

      case "rpcError":
        resolveRpc(message, false);
        break;

      case "showError":
        setStatus(message.message || "Error");
        break;
    }
  });

  if (openTextButton) {
    openTextButton.addEventListener("click", () => {
      vscode.postMessage({ type: "openText" });
    });
  }

  if (saveButton) {
    saveButton.addEventListener("click", () => {
      vscode.postMessage({ type: "save" });
    });
  }

  vscode.postMessage({ type: "ready" });
})();
