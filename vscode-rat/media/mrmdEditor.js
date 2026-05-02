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
  let unwatchPageMode = null;
  let pageModePreference = "auto";
  let themeAssociations = defaultThemeAssociations();
  let pageCanvasBackground = "auto";
  let markdownFontScale = 1;
  let applyingHostUpdate = false;
  let editTimer = 0;
  let lastSentText = "";
  let documentVersion = 0;
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

  const PAGE_VIEW_MIN_WIDTH = 980;

  const HOST_THEME_OVERRIDES = {
    "--widget-font-mono": "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
    "--widget-font-sans": "var(--vscode-font-family, system-ui, sans-serif)",
    "--editor-font-family": "var(--vscode-editor-font-family, var(--vscode-font-family, system-ui, sans-serif))",
    "--editor-font-size": "var(--rat-markdown-font-size, var(--vscode-editor-font-size, 14px))",
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

    "--widget-font-size": "var(--rat-markdown-font-size, var(--vscode-editor-font-size, 14px))",
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

  function defaultThemeAssociations() {
    return {
      light: "vscode-host-light",
      dark: "vscode-host-dark",
      highContrast: "vscode-host-dark",
      highContrastLight: "vscode-host-light",
    };
  }

  function normalizeThemeAssociations(value) {
    const defaults = defaultThemeAssociations();
    const input = value && typeof value === "object" ? value : {};
    return {
      light: typeof input.light === "string" && input.light ? input.light : defaults.light,
      dark: typeof input.dark === "string" && input.dark ? input.dark : defaults.dark,
      highContrast: typeof input.highContrast === "string" && input.highContrast ? input.highContrast : defaults.highContrast,
      highContrastLight: typeof input.highContrastLight === "string" && input.highContrastLight ? input.highContrastLight : defaults.highContrastLight,
    };
  }

  function hostThemeKind() {
    const cls = document.body.classList;
    if (cls.contains("vscode-high-contrast-light")) return "highContrastLight";
    if (cls.contains("vscode-high-contrast")) return "highContrast";
    if (cls.contains("vscode-dark")) return "dark";
    return "light";
  }

  function isDarkTheme() {
    const kind = hostThemeKind();
    return kind === "dark" || kind === "highContrast";
  }

  function hostThemeName() {
    const defaults = defaultThemeAssociations();
    const kind = hostThemeKind();
    return themeAssociations[kind] || defaults[kind] || (isDarkTheme() ? defaults.dark : defaults.light);
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

  function normalizePageMode(value) {
    return value === "always" || value === "never" || value === "auto" ? value : "auto";
  }

  function normalizeCanvasBackground(value) {
    return typeof value === "string" && value.trim() ? value.trim() : "auto";
  }

  function normalizeFontScale(value) {
    const n = Number(value);
    if (!Number.isFinite(n) || n <= 0) return 1;
    return Math.max(0.5, Math.min(2.5, n));
  }

  function fontSizeExpression(scale = markdownFontScale) {
    const normalized = normalizeFontScale(scale);
    return `calc(var(--vscode-editor-font-size, 14px) * ${normalized})`;
  }

  function codeFontSizeExpression(scale = markdownFontScale) {
    const normalized = normalizeFontScale(scale);
    return `calc(var(--vscode-editor-font-size, 14px) * ${normalized} * 0.92)`;
  }

  function resolveCanvasBackground(value) {
    const normalized = normalizeCanvasBackground(value);
    switch (normalized) {
      case "auto":
        return "color-mix(in srgb, var(--vscode-editor-background) 88%, var(--vscode-editor-foreground) 12%)";
      case "editor":
        return "var(--vscode-editor-background)";
      case "sideBar":
        return "var(--vscode-sideBar-background, var(--vscode-editor-background))";
      case "panel":
        return "var(--vscode-panel-background, var(--vscode-sideBar-background, var(--vscode-editor-background)))";
      case "transparent":
        return "transparent";
      default:
        return normalized;
    }
  }

  function applyHostCssVariables() {
    if (!root) return;
    root.style.setProperty("--rat-page-canvas-background", resolveCanvasBackground(pageCanvasBackground));
    root.style.setProperty("--rat-markdown-font-size", fontSizeExpression());
    root.style.setProperty("--rat-markdown-code-font-size", codeFontSizeExpression());
    root.style.setProperty(
      "--rat-page-shadow",
      isDarkTheme()
        ? "0 18px 60px rgba(0, 0, 0, 0.34)"
        : "0 18px 60px rgba(15, 23, 42, 0.14)",
    );
  }

  function shouldUsePagePresentation() {
    if (pageModePreference === "always") return true;
    if (pageModePreference === "never") return false;
    const width = root ? root.getBoundingClientRect().width : window.innerWidth;
    return width >= PAGE_VIEW_MIN_WIDTH;
  }

  function applyResponsivePresentation() {
    applyHostCssVariables();
    if (!editor || typeof editor.setDocumentPresentationMode !== "function") return;
    const next = shouldUsePagePresentation() ? "page" : "flow";
    if (!editor.getDocumentPresentationMode || editor.getDocumentPresentationMode() !== next) {
      editor.setDocumentPresentationMode(next);
    }
  }

  function watchPageMode() {
    if (!root || typeof ResizeObserver === "undefined") {
      window.addEventListener("resize", applyResponsivePresentation);
      return () => window.removeEventListener("resize", applyResponsivePresentation);
    }
    const observer = new ResizeObserver(() => applyResponsivePresentation());
    observer.observe(root);
    return () => observer.disconnect();
  }

  function createHostDocumentTemplate() {
    return {
      name: "VS Code",
      version: 1,
      editor: { applyDocumentStyles: true },
      page: {
        background: "var(--vscode-editor-background)",
        maxWidth: "",
        paperSize: "letter",
        marginTop: "0.8in",
        marginBottom: "0.8in",
        marginLeft: "0.9in",
        marginRight: "0.9in",
      },
      body: {
        color: "var(--vscode-editor-foreground)",
        fontFamily: "var(--vscode-editor-font-family, var(--vscode-font-family, system-ui, sans-serif))",
        fontSize: "var(--rat-markdown-font-size, var(--vscode-editor-font-size, 14px))",
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
          fontSize: "var(--rat-markdown-code-font-size, var(--rat-markdown-font-size, var(--vscode-editor-font-size, 14px)))",
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
  --rat-page-canvas-background: color-mix(in srgb, var(--vscode-editor-background) 88%, var(--vscode-editor-foreground) 12%);
  --rat-page-shadow: 0 18px 60px rgba(15, 23, 42, 0.14);
  --rat-markdown-font-size: var(--vscode-editor-font-size, 14px);
  --rat-markdown-code-font-size: calc(var(--rat-markdown-font-size) * 0.92);
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

#editor.mrmd-presentation-page {
  background: var(--rat-page-canvas-background) !important;
}

#editor.mrmd-presentation-page > .cm-editor {
  background: var(--vscode-editor-background) !important;
  border: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent));
  box-shadow: var(--rat-page-shadow) !important;
}

#editor.mrmd-presentation-page .cm-scroller {
  background: var(--vscode-editor-background) !important;
}

#editor .cm-content,
#editor .cm-line {
  color: var(--vscode-editor-foreground);
}

#editor .cm-content {
  font-size: var(--rat-markdown-font-size);
}

#editor .cm-codeblock,
#editor .cm-output,
#editor .cm-output-widget,
#editor .cm-cell-output,
#editor .mrmd-output {
  font-size: var(--rat-markdown-code-font-size);
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
    applyHostCssVariables();
    if (!editor) return;
    const nextTheme = hostThemeName();
    if (editor.setTheme) editor.setTheme(nextTheme);
    if (editor.setDocumentTemplate) {
      editor.setDocumentTemplate(createHostDocumentTemplate());
    }
    if (editor.view) {
      editor.view.dispatch({ effects: [] });
    }
    applyResponsivePresentation();
  }

  function scheduleHostThemeSync() {
    syncHostTheme();
    requestAnimationFrame(syncHostTheme);
    setTimeout(syncHostTheme, 50);
    setTimeout(syncHostTheme, 250);
  }

  function watchHostTheme() {
    const observer = new MutationObserver(scheduleHostThemeSync);
    observer.observe(document.body, { attributes: true, attributeFilter: ["class", "style"] });
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
      vscode.postMessage({ type: "edit", text, version: documentVersion });
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

  function isExpression(value, language) {
    const text = String(value || "").trim();
    if (!text || text.length > 200 || /^\d/.test(text) || text.includes("..")) return false;
    const lang = String(language || "").toLowerCase();
    if (lang === "javascript" || lang === "js" || lang === "node") {
      return /^[$A-Za-z_][\w$]*(?:\.[$A-Za-z_][\w$]*)*$/.test(text);
    }
    return /^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*$/.test(text);
  }

  function currentInspectionTarget() {
    if (!editor || !editor.view || !editor.getCells) return { language: null, expression: null };

    const state = editor.view.state;
    const selection = state.selection.main;
    const pos = selection.head;
    const cell = editor.getCells().find((candidate) =>
      candidate.executable && pos >= candidate.codeStart && pos <= candidate.codeEnd
    );

    if (!cell) return { language: null, expression: null };

    let expression = null;
    if (!selection.empty) {
      const selected = state.sliceDoc(selection.from, selection.to).trim();
      if (isExpression(selected, cell.language)) expression = selected;
    }

    if (!expression) {
      const offset = Math.max(0, Math.min(pos - cell.codeStart, cell.code.length));
      expression = wordAt(cell.code, offset);
      if (!isExpression(expression, cell.language)) expression = null;
    }

    return { language: cell.baseLanguage || cell.language, expression };
  }

  let selectionTimer = 0;
  function postSelection() {
    if (selectionTimer) clearTimeout(selectionTimer);
    selectionTimer = setTimeout(() => {
      selectionTimer = 0;
      const target = currentInspectionTarget();
      vscode.postMessage({
        type: "selection",
        language: target.language,
        expression: target.expression,
      });
    }, 80);
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

    pageModePreference = normalizePageMode(message.pageMode);
    themeAssociations = normalizeThemeAssociations(message.themeAssociations);
    pageCanvasBackground = normalizeCanvasBackground(message.canvasBackground);
    markdownFontScale = normalizeFontScale(message.fontScale);

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
      documentPresentationMode: "flow",
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
    if (unwatchPageMode) unwatchPageMode();
    unwatchTheme = watchHostTheme();
    unwatchPageMode = watchPageMode();
    syncHostTheme();
    applyResponsivePresentation();

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

    editor.onSelectionChange(postSelection);

    lastSentText = message.text || "";
    documentVersion = Number(message.version) || 0;
    setStatus("Ready");
    editor.focus();
    postSelection();
  }

  window.addEventListener("message", (event) => {
    const message = event.data || {};

    switch (message.type) {
      case "init":
        createEditor(message);
        break;

      case "setContent":
        if (!editor) return;
        documentVersion = Number(message.version) || documentVersion;
        if (message.text === editor.getContent()) return;
        applyingHostUpdate = true;
        editor.setContent(message.text || "");
        lastSentText = message.text || "";
        setTimeout(() => { applyingHostUpdate = false; }, 0);
        postSelection();
        break;

      case "themeChanged":
        scheduleHostThemeSync();
        break;

      case "pageModeChanged":
        pageModePreference = normalizePageMode(message.pageMode);
        applyResponsivePresentation();
        break;

      case "appearanceChanged":
        pageModePreference = normalizePageMode(message.pageMode);
        themeAssociations = normalizeThemeAssociations(message.themeAssociations);
        pageCanvasBackground = normalizeCanvasBackground(message.canvasBackground);
        markdownFontScale = normalizeFontScale(message.fontScale);
        scheduleHostThemeSync();
        applyResponsivePresentation();
        break;

      case "editApplied":
        documentVersion = Number(message.version) || documentVersion;
        if (typeof message.text === "string") lastSentText = message.text;
        break;

      case "editConflict":
        if (!editor) return;
        documentVersion = Number(message.version) || documentVersion;
        applyingHostUpdate = true;
        editor.setContent(message.text || "");
        lastSentText = message.text || "";
        setStatus("Document changed externally; reloaded.");
        setTimeout(() => { applyingHostUpdate = false; }, 0);
        postSelection();
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
