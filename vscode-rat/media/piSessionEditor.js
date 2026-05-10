/* global acquireVsCodeApi, mrmd */
(function () {
  "use strict";
const vscode = acquireVsCodeApi();
const persistedState = vscode.getState?.() || {};
const root = document.getElementById("session");
const status = document.getElementById("status");
const reloadButton = document.getElementById("reload");
const newSessionButton = document.getElementById("new-session");
const allSessionsButton = document.getElementById("all-sessions");
const treeButton = document.getElementById("tree-button");
const modelButton = document.getElementById("model-button");
const thinkingButton = document.getElementById("thinking-button");
const authButton = document.getElementById("auth-button");
const sendButton = document.getElementById("send");
const composeRoot = document.getElementById("compose");
const goHomeButton = document.getElementById("go-home");
const homeView = document.getElementById("home-view");
const editorView = document.getElementById("editor-view");
const sessionList = document.getElementById("session-list");
const searchInput = document.getElementById("search");
const viewTitle = document.getElementById("view-title");
const sessionNameInput = document.getElementById("session-name");
const modeSelect = document.getElementById("mode-select");
const modeDialog = document.getElementById("mode-dialog");
const modeForm = document.getElementById("mode-form");
const modeDialogTitle = document.getElementById("mode-dialog-title");
const modeKeyInput = document.getElementById("mode-key");
const modeLabelInput = document.getElementById("mode-label");
const modeOpenerInput = document.getElementById("mode-opener");
const modeAppendixInput = document.getElementById("mode-appendix");
const modeSystemPromptInput = document.getElementById("mode-system-prompt");
const modeSectionInputs = Array.from(document.querySelectorAll("[data-mode-section]"));
const modeCancelButton = document.getElementById("mode-cancel");
const treeDialog = document.getElementById("tree-dialog");
const treeCloseButton = document.getElementById("tree-close");
const treeSearchInput = document.getElementById("tree-search");
const treeFilterSelect = document.getElementById("tree-filter");
const treeList = document.getElementById("tree-list");
const treeDetails = document.getElementById("tree-details");
const treeContinueButton = document.getElementById("tree-continue");
const treeLabelInput = document.getElementById("tree-label");
const treeSaveLabelButton = document.getElementById("tree-save-label");
const treeClearLabelButton = document.getElementById("tree-clear-label");
const treeSummarySelect = document.getElementById("tree-summary");
const treeCustomSummaryInput = document.getElementById("tree-custom-summary");
const modelDialog = document.getElementById("model-dialog");
const modelCloseButton = document.getElementById("model-close");
const modelSearchInput = document.getElementById("model-search");
const modelList = document.getElementById("model-list");
const modelDetails = document.getElementById("model-details");
const thinkingDialog = document.getElementById("thinking-dialog");
const thinkingCloseButton = document.getElementById("thinking-close");
const thinkingList = document.getElementById("thinking-list");
const authDialog = document.getElementById("auth-dialog");
const authCloseButton = document.getElementById("auth-close");
const authSearchInput = document.getElementById("auth-search");
const authList = document.getElementById("auth-list");
const authDetails = document.getElementById("auth-details");
const authApiKeyInput = document.getElementById("auth-api-key");
const authSaveKeyButton = document.getElementById("auth-save-key");
const authLoginOAuthButton = document.getElementById("auth-login-oauth");
const authLogoutButton = document.getElementById("auth-logout");
const authPromptBox = document.getElementById("auth-prompt-box");
const authPromptText = document.getElementById("auth-prompt-text");
const authPromptInput = document.getElementById("auth-prompt-input");
const authPromptSubmit = document.getElementById("auth-prompt-submit");

const ADD_MODE_VALUE = "__pi_add_mode__";
const EDIT_MODE_VALUE = "__pi_edit_mode__";

let modeOptions = [];

let nextId = 1;
let allSessions = [];
let sessionScope = "workspace";
let sessionLimit = 200;
const pendingRuns = new Map();
const pendingRpcs = new Map();
const editors = new Map();
const editTimers = new Map();
let composeEditor = null;
let composeDraft = persistedState.composeDraft || "";
let composeHadFocus = false;
let streamingEditor = null;
let streamingText = "";
let currentCwd = null;
let activeEntryId = null;
let currentMode = "";
let currentSystemPrompt = "";
let currentSessionName = "";
let treeState = null;
let selectedTreeId = null;
let expandedTreeIds = new Set();
let isPiRunning = false;
let controlsState = null;
let modelState = null;
let selectedModelKey = null;
let authState = null;
let selectedAuthProviderKey = null;
let pendingAuthPromptId = null;

const HOST_THEME_OVERRIDES = {
  "--widget-font-mono": "var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace)",
  "--widget-font-sans": "var(--vscode-font-family, system-ui, sans-serif)",
  "--editor-font-family": "var(--vscode-font-family, system-ui, sans-serif)",
  "--editor-font-size": "var(--vscode-editor-font-size, 14px)",
  "--editor-background": "var(--vscode-editor-background)",
  "--editor-foreground": "var(--vscode-editor-foreground)",
  "--editor-cursor": "var(--vscode-editorCursor-foreground)",
  "--editor-selection": "var(--vscode-editor-selectionBackground)",
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
  "--mrmd-bg": "var(--vscode-editor-background)",
  "--mrmd-fg": "var(--vscode-editor-foreground)",
  "--mrmd-fg-muted": "var(--vscode-descriptionForeground)",
  "--mrmd-border": "var(--vscode-editorWidget-border, var(--vscode-panel-border, var(--widget-border)))",
  "--mrmd-popup-bg": "var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)))",
  "--mrmd-hover-bg": "var(--vscode-list-hoverBackground, var(--widget-surface-hover))",
  "--mrmd-selection-bg": "var(--vscode-editor-selectionBackground)",
  "--mrmd-accent": "var(--vscode-focusBorder, var(--vscode-textLink-foreground))",
  "--syntax-keyword": "var(--vscode-symbolIcon-keywordForeground, var(--vscode-charts-purple, var(--vscode-editor-foreground)))",
  "--syntax-string": "var(--vscode-symbolIcon-stringForeground, var(--vscode-charts-green, var(--vscode-editor-foreground)))",
  "--syntax-number": "var(--vscode-symbolIcon-numberForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)))",
  "--syntax-comment": "var(--vscode-editorCodeLens-foreground, var(--vscode-descriptionForeground))",
  "--syntax-function": "var(--vscode-symbolIcon-functionForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
  "--syntax-variable": "var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground))",
  "--syntax-type": "var(--vscode-symbolIcon-classForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
  "--syntax-operator": "var(--vscode-symbolIcon-operatorForeground, var(--vscode-editor-foreground))",
  "--syntax-punctuation": "var(--vscode-editor-foreground)",
  "--syntax-property": "var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground))",
  "--syntax-constant": "var(--vscode-symbolIcon-constantForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)))",
  "--syntax-tag": "var(--vscode-symbolIcon-structForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)))",
  "--syntax-attribute": "var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground))",
  "--syntax-meta": "var(--vscode-descriptionForeground)",
  "--md-heading-color": "var(--vscode-editor-foreground)",
  "--md-link-color": "var(--vscode-textLink-foreground)",
  "--md-code-background": "var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent))",
  "--md-code-color": "var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground))",
  "--md-blockquote-border": "var(--vscode-textBlockQuote-border, var(--vscode-focusBorder))",
  "--md-blockquote-color": "var(--vscode-descriptionForeground)",
  "--md-marker-color": "var(--vscode-descriptionForeground)",
  "--md-hr-color": "var(--vscode-panel-border, var(--widget-border))",
};

function hostThemeKind() {
  const cls = document.body.classList;
  if (cls.contains("vscode-high-contrast-light")) return "highContrastLight";
  if (cls.contains("vscode-high-contrast")) return "highContrast";
  if (cls.contains("vscode-dark")) return "dark";
  return "light";
}

function hostThemeName() {
  const kind = hostThemeKind();
  return kind === "dark" || kind === "highContrast" ? "vscode-host-dark" : "vscode-host-light";
}

function registerHostThemes() {
  const widgets = (window.mrmd && window.mrmd.widgets) || window.mrmd;
  if (!widgets?.createTheme || !widgets?.registerTheme) return;
  widgets.registerTheme(widgets.createTheme({ name: "vscode-host-light", base: "plain-light", description: "VS Code light theme bridge", overrides: HOST_THEME_OVERRIDES }));
  widgets.registerTheme(widgets.createTheme({ name: "vscode-host-dark", base: "plain-dark", description: "VS Code dark theme bridge", overrides: HOST_THEME_OVERRIDES }));
}

function createHostDocumentTemplate() {
  return {
    name: "VS Code",
    version: 1,
    editor: { applyDocumentStyles: true },
    body: {
      color: "var(--vscode-editor-foreground)",
      fontFamily: "var(--vscode-font-family, system-ui, sans-serif)",
      fontSize: "var(--vscode-editor-font-size, 14px)",
      lineHeight: "1.55",
    },
    link: { color: "var(--vscode-textLink-foreground)", underline: true },
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
      highlight: {
        keyword: "var(--syntax-keyword)",
        controlKeyword: "var(--syntax-control)",
        string: "var(--syntax-string)",
        number: "var(--syntax-number)",
        comment: "var(--syntax-comment)",
        function: "var(--syntax-function)",
        variable: "var(--syntax-variable)",
        type: "var(--syntax-type)",
        operator: "var(--syntax-operator)",
        punctuation: "var(--syntax-punctuation)",
        property: "var(--syntax-property)",
        constant: "var(--syntax-constant)",
        regexp: "var(--syntax-string)",
        escape: "var(--syntax-string)",
        tag: "var(--syntax-tag)",
        attribute: "var(--syntax-attribute)",
        attributeValue: "var(--syntax-string)",
        meta: "var(--syntax-meta)",
        commentStyle: "italic",
      },
    },
  };
}

function syncEditorTheme(editor) {
  if (!editor) return;
  if (editor.setTheme) editor.setTheme(hostThemeName());
  if (editor.setDocumentTemplate) editor.setDocumentTemplate(createHostDocumentTemplate());
  if (editor.view) editor.view.dispatch({ effects: [] });
}

function syncAllEditorThemes() {
  registerHostThemes();
  editors.forEach(syncEditorTheme);
  syncEditorTheme(composeEditor);
  syncEditorTheme(streamingEditor);
}

function makeReadOnly(editor) {
  const cm = window.mrmd && window.mrmd.codemirror;
  if (!editor?.view || !cm?.StateEffect) return;
  const extensions = [];
  if (cm.EditorState?.readOnly) extensions.push(cm.EditorState.readOnly.of(true));
  if (cm.EditorView?.editable) extensions.push(cm.EditorView.editable.of(false));
  if (extensions.length) {
    editor.view.dispatch({ effects: cm.StateEffect.appendConfig.of(extensions) });
  }
  editor.view.dom.setAttribute("aria-readonly", "true");
  editor.view.contentDOM.setAttribute("aria-readonly", "true");
}

function blurInactiveEditor(mount) {
  if (!mount || mount.closest(".editable")) return;
  const active = document.activeElement;
  if (active instanceof HTMLElement && mount.contains(active)) active.blur();
  mount.querySelector(".cm-content")?.blur?.();
  mount.querySelector(".cm-editor")?.classList.remove("cm-focused");
}

function setStatus(text) {
  if (status) status.textContent = text || "";
}

function modeLabel(mode) {
  return modeOptions.find((option) => option.value === mode)?.label || mode;
}

function setModeOptions(options) {
  modeOptions = Array.isArray(options) ? options.filter((option) => option?.value) : [];
  if (!modeSelect) return;
  modeSelect.textContent = "";
  for (const option of modeOptions) {
    const el = document.createElement("option");
    el.value = option.value;
    el.textContent = option.label || option.value;
    modeSelect.appendChild(el);
  }
  if (modeOptions.length) {
    const separator = document.createElement("option");
    separator.disabled = true;
    separator.textContent = "────────────";
    modeSelect.appendChild(separator);

    const edit = document.createElement("option");
    edit.value = EDIT_MODE_VALUE;
    edit.textContent = "Edit current mode…";
    modeSelect.appendChild(edit);

    const add = document.createElement("option");
    add.value = ADD_MODE_VALUE;
    add.textContent = "+ Add mode…";
    modeSelect.appendChild(add);
  }
}

function modeKeyFromLabel(label) {
  return String(label || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .replace(/^[^a-z]+/, "")
    .slice(0, 48);
}

function openModeDialog(definition, isEdit) {
  if (!modeDialog || !modeForm) return;
  modeDialogTitle.textContent = isEdit ? "Edit mode" : "Add mode";
  modeKeyInput.value = definition?.key || "";
  modeKeyInput.disabled = !!isEdit && !!definition?.key;
  modeLabelInput.value = definition?.label || "";
  modeOpenerInput.value = definition?.opener || "";
  modeAppendixInput.value = definition?.appendix || "";
  modeSystemPromptInput.value = definition?.systemPrompt || "";
  const removed = new Set(definition?.removeSections || []);
  for (const input of modeSectionInputs) input.checked = removed.has(input.dataset.modeSection);
  modeDialog.classList.remove("hidden");
  modeLabelInput.focus();
}

function closeModeDialog() {
  modeDialog?.classList.add("hidden");
  if (modeSelect) modeSelect.value = currentMode;
}

async function saveModeFromDialog() {
  const definition = {
    key: modeKeyInput.value || modeKeyFromLabel(modeLabelInput.value),
    label: modeLabelInput.value,
    opener: modeOpenerInput.value,
    appendix: modeAppendixInput.value,
    systemPrompt: modeSystemPromptInput.value,
    removeSections: modeSectionInputs.filter((input) => input.checked).map((input) => input.dataset.modeSection),
  };
  const result = await rpc("saveModeDefinition", { modeDefinition: definition }, 30_000);
  setModeOptions(result?.modes || modeOptions);
  currentMode = result?.mode || definition.key;
  updateModeUi(currentMode, true);
  vscode.postMessage({ type: "setMode", mode: currentMode });
  closeModeDialog();
}

function updateSessionNameUi(name, enabled) {
  currentSessionName = String(name || "").trim();
  if (!sessionNameInput) return;
  sessionNameInput.value = currentSessionName || "Untitled";
  sessionNameInput.disabled = !enabled;
  sessionNameInput.title = enabled ? "Session name — click to edit" : "Open or create a session to name it";
}

function saveSessionName() {
  if (!sessionNameInput || sessionNameInput.disabled) return;
  const next = sessionNameInput.value.trim() || "Untitled";
  if (next === (currentSessionName || "Untitled")) {
    updateSessionNameUi(currentSessionName, true);
    return;
  }
  currentSessionName = next;
  sessionNameInput.value = next;
  vscode.postMessage({ type: "setSessionName", sessionName: next });
  setStatus("Session name saved");
}

function updateModeUi(mode, enabled) {
  if (typeof mode === "string" && mode) currentMode = mode;
  if (!modeSelect) return;
  if (currentMode && modeOptions.length && !modeOptions.some((option) => option.value === currentMode)) {
    const el = document.createElement("option");
    el.value = currentMode;
    el.textContent = currentMode;
    modeSelect.appendChild(el);
  }
  modeSelect.value = currentMode;
  modeSelect.disabled = !enabled || modeOptions.length === 0;
  modeSelect.title = enabled && modeOptions.length ? "Prompt mode: " + modeLabel(currentMode) : "Open or create a session to choose prompt mode";
}

function showHome() {
  homeView.classList.add("active");
  editorView.classList.remove("active");
  goHomeButton.classList.add("hidden");
  viewTitle.textContent = "Sessions";
  updateSessionNameUi("", false);
  updateModeUi(currentMode, false);
  if (treeButton) treeButton.disabled = true;
  if (allSessionsButton) allSessionsButton.classList.remove("hidden");
  updateControlsUi(controlsState);
  vscode.postMessage({ type: "openSession", path: null });
}

function showEditor(_title) {
  homeView.classList.remove("active");
  editorView.classList.add("active");
  goHomeButton.classList.remove("hidden");
  viewTitle.textContent = "Pi";
  updateSessionNameUi(currentSessionName, true);
  updateModeUi(currentMode, true);
  if (treeButton) treeButton.disabled = false;
  if (allSessionsButton) allSessionsButton.classList.add("hidden");
  updateControlsUi(controlsState);
  void refreshControls();
}

function treeNodeMap() {
  const map = new Map();
  const visit = (node, depth, parent) => {
    map.set(node.id, { node, depth, parent });
    for (const child of node.children || []) visit(child, depth + 1, node.id);
  };
  for (const rootNode of treeState?.roots || []) visit(rootNode, 0, null);
  return map;
}

function flattenTreeNodes() {
  const map = treeNodeMap();
  const activePath = new Set(treeState?.activePathIds || []);
  const q = (treeSearchInput?.value || "").toLowerCase().trim();
  const tokens = q.split(/\s+/).filter(Boolean);
  const filter = treeFilterSelect?.value || "default";
  const rows = [];
  const visit = (node, depth, ancestry) => {
    const hasChildren = !!node.children?.length;
    const hiddenByFold = ancestry.some((id) => !expandedTreeIds.has(id));
    const searchable = [node.preview, node.markdown, node.role, node.type, node.label].filter(Boolean).join(" ").toLowerCase();
    let visible = !hiddenByFold && tokens.every((token) => searchable.includes(token));
    const isSettings = ["label", "custom", "model_change", "thinking_level_change", "session_info"].includes(node.type);
    if (filter === "user-only") visible = visible && node.type === "message" && node.role === "user";
    else if (filter === "no-tools") visible = visible && !isSettings && !(node.type === "message" && node.role.startsWith("tool:"));
    else if (filter === "labeled-only") visible = visible && !!node.label;
    else if (filter !== "all") visible = visible && !isSettings;
    if (visible) rows.push({ node, depth, active: activePath.has(node.id), isLeaf: node.id === treeState?.leafId, hasChildren });
    if (!hiddenByFold) {
      for (const child of node.children || []) visit(child, depth + 1, [...ancestry, node.id]);
    }
  };
  for (const rootNode of treeState?.roots || []) visit(rootNode, 0, []);
  return rows;
}

function openTreeDialog() {
  if (!treeDialog) return;
  treeDialog.classList.remove("hidden");
  selectedTreeId = null;
  treeDetails.innerHTML = `<p class="muted">Loading conversation tree…</p>`;
  treeList.textContent = "";
  rpc("getTree", {}, 30_000).then((tree) => {
    treeState = tree || { roots: [], leafId: null, activePathIds: [] };
    expandedTreeIds = new Set((treeState.activePathIds || []).concat((treeState.roots || []).map((n) => n.id)));
    selectedTreeId = treeState.leafId || treeState.roots?.[0]?.id || null;
    renderTreeView();
  }).catch((error) => {
    treeDetails.innerHTML = `<p class="error"></p>`;
    treeDetails.querySelector(".error").textContent = error.message || "Could not load tree";
  });
}

function closeTreeDialog() {
  treeDialog?.classList.add("hidden");
}

function renderTreeView() {
  if (!treeList || !treeDetails) return;
  const rows = flattenTreeNodes();
  treeList.textContent = "";
  const map = treeNodeMap();
  if (selectedTreeId && !map.has(selectedTreeId)) selectedTreeId = rows[0]?.node.id || null;
  for (const row of rows) {
    const button = document.createElement("button");
    button.className = "tree-node" + (row.node.id === selectedTreeId ? " selected" : "") + (row.active ? " active" : "") + (row.isLeaf ? " leaf" : "");
    button.style.paddingLeft = `${8 + row.depth * 18}px`;
    button.dataset.id = row.node.id;
    const toggle = document.createElement("span");
    toggle.className = "tree-toggle";
    toggle.textContent = row.hasChildren ? (expandedTreeIds.has(row.node.id) ? "▾" : "▸") : "•";
    const label = document.createElement("span");
    label.className = "tree-node-label role-" + row.node.role.replace(/[^a-z0-9_-]/gi, "-");
    label.textContent = row.node.preview || `[${row.node.type}]`;
    const badges = document.createElement("span");
    badges.className = "tree-badges";
    if (row.node.label) {
      const badge = document.createElement("span");
      badge.className = "tree-badge label";
      badge.textContent = row.node.label;
      badges.appendChild(badge);
    }
    if (row.isLeaf) {
      const badge = document.createElement("span");
      badge.className = "tree-badge leaf";
      badge.textContent = "leaf";
      badges.appendChild(badge);
    }
    button.append(toggle, label, badges);
    toggle.addEventListener("click", (event) => {
      event.stopPropagation();
      if (!row.hasChildren) return;
      expandedTreeIds.has(row.node.id) ? expandedTreeIds.delete(row.node.id) : expandedTreeIds.add(row.node.id);
      renderTreeView();
    });
    button.addEventListener("click", () => {
      selectedTreeId = row.node.id;
      renderTreeView();
    });
    button.addEventListener("dblclick", () => navigateSelectedTreeNode());
    treeList.appendChild(button);
  }
  renderTreeDetails(map.get(selectedTreeId)?.node || rows[0]?.node || null, rows.length);
}

function renderTreeDetails(node, visibleCount) {
  const disabled = !node || isPiRunning;
  treeContinueButton.disabled = disabled || node.id === treeState?.leafId;
  treeSaveLabelButton.disabled = !node;
  treeClearLabelButton.disabled = !node || !node.label;
  treeLabelInput.disabled = !node;
  treeSummarySelect.disabled = disabled || node.id === treeState?.leafId;
  treeCustomSummaryInput.disabled = disabled || treeSummarySelect.value !== "custom";
  if (!node) {
    treeDetails.innerHTML = `<p class="muted">No entries. ${visibleCount || 0} visible.</p>`;
    return;
  }
  treeLabelInput.value = node.label || "";
  treeContinueButton.textContent = node.type === "message" && node.role === "user" || node.type === "custom_message" ? "Edit and branch from here" : "Continue from here";
  treeDetails.textContent = "";
  const meta = document.createElement("div");
  meta.className = "tree-meta";
  meta.textContent = `${node.type} · ${node.role} · ${new Date(node.timestamp).toLocaleString()} · ${visibleCount} visible`;
  const pre = document.createElement("pre");
  pre.textContent = node.markdown || node.preview || "(no content)";
  treeDetails.append(meta, pre);
  if (isPiRunning) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "Pi is responding; tree changes are disabled until it finishes.";
    treeDetails.prepend(p);
  }
}

async function saveTreeLabel(clear) {
  if (!selectedTreeId) return;
  const result = await rpc("setTreeLabel", { targetId: selectedTreeId, label: clear ? "" : treeLabelInput.value }, 30_000);
  treeState = result;
  renderTreeView();
  setStatus(clear ? "Label cleared" : "Label saved");
}

async function navigateSelectedTreeNode() {
  if (!selectedTreeId || isPiRunning) return;
  const summaryMode = treeSummarySelect?.value || "none";
  const payload = {
    targetId: selectedTreeId,
    summarize: summaryMode === "default" || summaryMode === "custom",
    customInstructions: summaryMode === "custom" ? treeCustomSummaryInput.value : undefined,
  };
  setStatus(payload.summarize ? "Summarizing branch…" : "Navigating tree…");
  const result = await rpc("navigateTree", payload, payload.summarize ? 180_000 : 30_000);
  if (result?.aborted || result?.cancelled) {
    setStatus(result.aborted ? "Branch summarization cancelled" : "Tree navigation cancelled");
    return;
  }
  if (typeof result?.editorText === "string" && result.editorText.trim()) {
    composeDraft = result.editorText;
    vscode.setState?.({ ...(vscode.getState?.() || {}), composeDraft });
    composeEditor?.setContent?.(result.editorText);
  }
  closeTreeDialog();
  setStatus("Navigated to selected point");
}

function updateControlsUi(state) {
  if (state) controlsState = state;
  const model = controlsState?.model;
  if (modelButton) {
    modelButton.textContent = model ? `${model.provider}/${model.id}` : "Model";
    modelButton.disabled = isPiRunning || !editorView.classList.contains("active");
    modelButton.title = model?.name || "Select model";
  }
  if (thinkingButton) {
    const levels = controlsState?.availableThinkingLevels || [];
    thinkingButton.textContent = controlsState?.supportsThinking ? `Think: ${controlsState.thinkingLevel}` : "Think: off";
    thinkingButton.disabled = isPiRunning || !editorView.classList.contains("active") || levels.length === 0;
  }
  if (authButton) authButton.disabled = isPiRunning || !editorView.classList.contains("active");
}

async function refreshControls() {
  try {
    updateControlsUi(await rpc("getRuntimeControlsState", {}, 30_000));
  } catch (error) {
    setStatus(error.message || "Could not load model state");
  }
}

function closeModelDialog() { modelDialog?.classList.add("hidden"); }
function closeThinkingDialog() { thinkingDialog?.classList.add("hidden"); }
function closeAuthDialog() { authDialog?.classList.add("hidden"); }

async function openModelDialog() {
  if (!modelDialog) return;
  modelDialog.classList.remove("hidden");
  modelList.textContent = "";
  modelDetails.innerHTML = `<p class="muted">Loading models…</p>`;
  modelState = await rpc("listModels", {}, 30_000);
  selectedModelKey = modelState.current ? `${modelState.current.provider}/${modelState.current.id}` : null;
  renderModelList();
}

function renderModelList() {
  if (!modelList || !modelState) return;
  const q = (modelSearchInput?.value || "").toLowerCase().trim();
  const tokens = q.split(/\s+/).filter(Boolean);
  const currentKey = modelState.current ? `${modelState.current.provider}/${modelState.current.id}` : "";
  const models = (modelState.models || []).filter((model) => {
    const hay = [model.provider, model.providerName, model.id, model.name].join(" ").toLowerCase();
    return tokens.every((token) => hay.includes(token));
  });
  modelList.textContent = "";
  for (const model of models) {
    const key = `${model.provider}/${model.id}`;
    const button = document.createElement("button");
    button.className = "picker-row" + (key === selectedModelKey ? " selected" : "") + (key === currentKey ? " current" : "") + (!model.available ? " unavailable" : "");
    button.innerHTML = `<span class="picker-title"></span><span class="picker-meta"></span>`;
    button.querySelector(".picker-title").textContent = model.id + (key === currentKey ? " ✓" : "");
    button.querySelector(".picker-meta").textContent = `${model.providerName || model.provider}${model.reasoning ? " · reasoning" : ""}${model.available ? "" : " · no auth"}`;
    button.addEventListener("click", () => { selectedModelKey = key; renderModelList(); });
    button.addEventListener("dblclick", () => selectCurrentModel());
    modelList.appendChild(button);
  }
  renderModelDetails(models.find((model) => `${model.provider}/${model.id}` === selectedModelKey) || models[0]);
}

function renderModelDetails(model) {
  if (!modelDetails) return;
  if (!model) { modelDetails.innerHTML = `<p class="muted">No matching models.</p>`; return; }
  selectedModelKey = `${model.provider}/${model.id}`;
  modelDetails.innerHTML = `
    <h3></h3>
    <p class="muted model-provider"></p>
    <dl class="details-grid">
      <dt>Status</dt><dd class="model-status"></dd>
      <dt>Context</dt><dd>${model.contextWindow || "unknown"}</dd>
      <dt>Max output</dt><dd>${model.maxTokens || "unknown"}</dd>
      <dt>Input</dt><dd>${(model.input || []).join(", ")}</dd>
    </dl>
    <button id="model-select-current" class="primary">Use this model</button>
  `;
  modelDetails.querySelector("h3").textContent = model.name || model.id;
  modelDetails.querySelector(".model-provider").textContent = `${model.providerName || model.provider} · ${model.provider}/${model.id}`;
  modelDetails.querySelector(".model-status").textContent = model.available ? "Available" : `Auth needed (${model.authStatus?.source || "unconfigured"})`;
  modelDetails.querySelector("#model-select-current").disabled = !model.available || isPiRunning;
  modelDetails.querySelector("#model-select-current").addEventListener("click", selectCurrentModel);
}

async function selectCurrentModel() {
  const [providerId, ...rest] = String(selectedModelKey || "").split("/");
  const modelId = rest.join("/");
  if (!providerId || !modelId) return;
  setStatus("Switching model…");
  const state = await rpc("setModel", { providerId, modelId }, 30_000);
  updateControlsUi(state);
  closeModelDialog();
  setStatus(`Model: ${modelId}`);
}

function openThinkingDialog() {
  if (!thinkingDialog) return;
  thinkingDialog.classList.remove("hidden");
  renderThinkingList();
}

function renderThinkingList() {
  if (!thinkingList) return;
  const descriptions = { off: "No reasoning", minimal: "Very brief reasoning", low: "Light reasoning", medium: "Moderate reasoning", high: "Deep reasoning", xhigh: "Maximum reasoning" };
  thinkingList.textContent = "";
  for (const level of controlsState?.availableThinkingLevels || []) {
    const button = document.createElement("button");
    button.className = "picker-row" + (level === controlsState?.thinkingLevel ? " current selected" : "");
    button.innerHTML = `<span class="picker-title"></span><span class="picker-meta"></span>`;
    button.querySelector(".picker-title").textContent = level + (level === controlsState?.thinkingLevel ? " ✓" : "");
    button.querySelector(".picker-meta").textContent = descriptions[level] || "Reasoning level";
    button.addEventListener("click", async () => {
      const state = await rpc("setThinkingLevel", { value: level }, 30_000);
      updateControlsUi(state);
      closeThinkingDialog();
      setStatus(`Thinking level: ${level}`);
    });
    thinkingList.appendChild(button);
  }
  if (!thinkingList.children.length) thinkingList.innerHTML = `<p class="muted">Current model does not expose thinking controls.</p>`;
}

async function openAuthDialog() {
  if (!authDialog) return;
  authDialog.classList.remove("hidden");
  authDetails.innerHTML = `<p class="muted">Loading providers…</p>`;
  authState = await rpc("listAuthProviders", {}, 30_000);
  selectedAuthProviderKey = authState.providers?.[0]?.key || null;
  renderAuthList();
}

function renderAuthList() {
  if (!authList || !authState) return;
  const q = (authSearchInput?.value || "").toLowerCase().trim();
  const tokens = q.split(/\s+/).filter(Boolean);
  const providers = (authState.providers || []).filter((provider) => tokens.every((token) => [provider.name, provider.id, provider.authType, provider.status?.source].join(" ").toLowerCase().includes(token)));
  authList.textContent = "";
  for (const provider of providers) {
    const button = document.createElement("button");
    button.className = "picker-row" + (provider.key === selectedAuthProviderKey ? " selected" : "") + (provider.configured ? " current" : "");
    button.innerHTML = `<span class="picker-title"></span><span class="picker-meta"></span>`;
    button.querySelector(".picker-title").textContent = provider.name + (provider.configured ? " ✓" : "");
    button.querySelector(".picker-meta").textContent = `${provider.id} · ${provider.authType}${provider.status?.source ? " · " + provider.status.source : ""}`;
    button.addEventListener("click", () => { selectedAuthProviderKey = provider.key; renderAuthList(); });
    authList.appendChild(button);
  }
  renderAuthDetails(providers.find((provider) => provider.key === selectedAuthProviderKey) || providers[0]);
}

function renderAuthDetails(provider) {
  if (!authDetails) return;
  const hasProvider = !!provider;
  authSaveKeyButton.disabled = !hasProvider || provider.authType !== "api_key" || isPiRunning;
  authLoginOAuthButton.disabled = !hasProvider || provider.authType !== "oauth" || isPiRunning;
  authLogoutButton.disabled = !hasProvider || !provider.configured || isPiRunning;
  authApiKeyInput.disabled = !hasProvider || provider.authType !== "api_key" || isPiRunning;
  if (!provider) { authDetails.innerHTML = `<p class="muted">No providers.</p>`; return; }
  selectedAuthProviderKey = provider.key;
  authDetails.innerHTML = `<h3></h3><p class="muted"></p><p></p>`;
  authDetails.querySelector("h3").textContent = provider.name;
  authDetails.querySelector(".muted").textContent = `${provider.id} · ${provider.authType}`;
  authDetails.querySelector("p:last-child").textContent = provider.configured ? `Configured via ${provider.status?.source || provider.storedType || "stored credentials"}` : "Not configured";
}

async function saveApiKey() {
  const provider = (authState?.providers || []).find((candidate) => candidate.key === selectedAuthProviderKey);
  if (!provider) return;
  const result = await rpc("loginApiKey", { providerId: provider.id, apiKey: authApiKeyInput.value }, 30_000);
  authApiKeyInput.value = "";
  authState = result.auth;
  updateControlsUi(result.controls);
  renderAuthList();
  setStatus("API key saved");
}

async function logoutProvider() {
  const provider = (authState?.providers || []).find((candidate) => candidate.key === selectedAuthProviderKey);
  if (!provider) return;
  const result = await rpc("logoutProvider", { providerId: provider.id }, 30_000);
  authState = result.auth;
  updateControlsUi(result.controls);
  renderAuthList();
  setStatus("Logged out");
}

async function loginOAuth() {
  const provider = (authState?.providers || []).find((candidate) => candidate.key === selectedAuthProviderKey);
  if (!provider) return;
  authPromptBox?.classList.add("hidden");
  setStatus("Starting login…");
  const result = await rpc("loginOAuth", { providerId: provider.id }, 180_000);
  authState = result.auth;
  updateControlsUi(result.controls);
  renderAuthList();
  setStatus("Login complete");
}

function renderSessions() {
  const q = (searchInput?.value || "").toLowerCase().trim();
  const filtered = !q ? allSessions : allSessions.filter((s) =>
    (s.label || "").toLowerCase().includes(q) ||
    (s.path || "").toLowerCase().includes(q) ||
    (s.cwd || "").toLowerCase().includes(q)
  );
  sessionList.textContent = "";
  for (const session of filtered) {
    const row = document.createElement("button");
    row.className = "session-item";
    row.innerHTML = `
      <span class="title"></span>
      <span class="meta"></span>
    `;
    row.querySelector(".title").textContent = session.label || "Untitled session";
    row.querySelector(".meta").textContent = [session.time, session.cwd].filter(Boolean).join(" · ");
    row.addEventListener("click", () => {
      vscode.postMessage({ type: "openSession", path: session.path });
    });
    sessionList.appendChild(row);
  }
  const scopeLabel = sessionScope === "all" ? "recent" : "workspace";
  const limitLabel = allSessions.length >= sessionLimit ? ` · latest ${sessionLimit}` : "";
  setStatus(`${filtered.length} ${scopeLabel} sessions${limitLabel}`);
}

  const supportedLanguages = [
    "python", "py", "python3",
    "r", "rlang",
    "bash", "sh", "shell", "zsh",
    "julia", "jl", "ju",
    "javascript", "js", "node",
    "pi", "slack",
  ];
  const supportedSet = new Set(supportedLanguages);

  function nextRequestId() {
    return nextId++;
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
    ok ? pending.resolve(message.result) : pending.reject(new Error(message.error || "Request failed"));
  }

  function isSupported(language) {
    return supportedSet.has(String(language || "").toLowerCase());
  }

  function parseCompletionLine(line) {
    const raw = String(line || "").trimEnd();
    if (!raw || raw.trim() === "No completions.") return null;
    const match = raw.match(/^(.*?)\t+([A-Za-z][\w-]*)$/) || raw.match(/^(.*?)\s{2,}([A-Za-z][\w-]*)$/);
    if (match) return { label: match[1].trimEnd(), kind: match[2].toLowerCase() };
    return { label: raw.trim(), kind: "variable" };
  }

  function completionReplaceStart(code, cursor, insertText) {
    let start = cursor;
    while (start > 0 && /[^\s|&;(){}\[\]<>'",]/.test(code[start - 1])) start--;
    const prefix = code.slice(start, cursor);
    if (prefix.includes(".") && !insertText.startsWith(prefix)) return start + prefix.lastIndexOf(".") + 1;
    return start;
  }

  function wordAt(code, cursor) {
    const text = String(code || "");
    let start = Math.max(0, Math.min(cursor, text.length));
    let end = start;
    while (start > 0 && /[\w.]/.test(text[start - 1])) start--;
    while (end < text.length && /[\w.]/.test(text[end])) end++;
    return text.slice(start, end).replace(/^\.+|\.+$/g, "") || null;
  }

  function splitRatAssets(text) {
    const assets = [];
    const kept = [];
    for (const line of String(text || "").split("\n")) {
      const match = line.match(/^__RAT_PLOT__:(.+)$/);
      if (match) assets.push(match[1].trim());
      else kept.push(line);
    }
    return { text: kept.join("\n"), assets };
  }

  const TOOL_PREVIEW_LINES = 10;

  function codeFence(text, language) {
    const value = String(text ?? "");
    const ticks = value.match(/`+/g)?.reduce((max, run) => Math.max(max, run.length), 2) ?? 2;
    const fence = "`".repeat(Math.max(3, ticks + 1));
    return `${fence}${language || ""}\n${value}\n${fence}`;
  }

  function limitedCodeFence(text, language, mode, summary) {
    const value = String(text ?? "").replace(/\s+$/, "");
    const lines = value.split(/\r?\n/);
    if (lines.length <= TOOL_PREVIEW_LINES) return codeFence(value, language);
    const preview = mode === "head" ? lines.slice(0, TOOL_PREVIEW_LINES) : lines.slice(-TOOL_PREVIEW_LINES);
    return `${codeFence(preview.join("\n"), language)}\n\n<details class="rat-collapsible-result"><summary>${summary}</summary>\n\n${codeFence(value, language)}\n</details>`;
  }

  function guessMime(url) {
    const lower = String(url || "").toLowerCase();
    if (lower.endsWith(".svg")) return "image/svg+xml";
    if (lower.endsWith(".jpg") || lower.endsWith(".jpeg")) return "image/jpeg";
    if (lower.endsWith(".gif")) return "image/gif";
    if (lower.endsWith(".webp")) return "image/webp";
    return "image/png";
  }

  function createRatRuntime(entryId) {
    const lspProvider = {
      languages: supportedLanguages,
      async complete(code, cursor, language) {
        if (!isSupported(language)) return { matches: [], cursorStart: cursor, cursorEnd: cursor, source: "runtime" };
        try {
          const result = await rpc("ratComplete", { entryId, code, cursor, language }, 10_000);
          const parsed = (result?.items || []).map(parseCompletionLine).filter(Boolean);
          let cursorStart = cursor;
          const matches = parsed.map((item) => {
            let insertText = item.label;
            if (insertText.endsWith("(")) insertText = insertText.slice(0, -1);
            cursorStart = Math.min(cursorStart, completionReplaceStart(code, cursor, insertText));
            return { label: item.label, insertText, kind: item.kind || "variable", detail: "rat" };
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
          const result = await rpc("ratInspect", { entryId, at, language }, 10_000);
          return result?.text ? { found: true, name: at, documentation: result.text } : null;
        } catch {
          return null;
        }
      },
      async inspect(code, cursor, language) {
        const at = wordAt(code, cursor);
        if (!at || !isSupported(language)) return null;
        try {
          const result = await rpc("ratInspect", { entryId, at, language }, 10_000);
          return result?.text ? { found: true, name: at, documentation: result.text, sourceCode: result.text } : null;
        } catch {
          return null;
        }
      },
      async listVariables() { return []; },
      async getVariable(name) { return { name, type: "unknown", value: "?" }; },
      async isComplete() { return { status: "unknown" }; },
      async format(code) { return { formatted: code, changed: false }; },
    };

    return {
      supports: isSupported,
      async execute(code, language) { return this.executeStreaming(code, language, function () {}, null, {}); },
      executeStreaming(code, language, onChunk, onStdinRequest, options) {
        if (!isSupported(language)) {
          const message = "No rat runtime for language: " + language;
          onChunk(message + "\n", message + "\n", true);
          return Promise.resolve({ success: false, stdout: "", stderr: message, error: { message } });
        }
        const id = nextRequestId();
        setStatus("Running " + language + " cell from " + entryId + "…");
        return new Promise((resolve, reject) => {
          pendingRuns.set(id, { entryId, onChunk, onAsset: options && options.onAsset, resolve, reject, accumulated: "" });
          vscode.postMessage({ type: "ratRun", id, entryId, code, language });
        });
      },
      getLSPProvider() { return lspProvider; },
    };
  }

  function installThemeStyle() {
    registerHostThemes();
    if (document.getElementById("pi-session-style")) return;
    const style = document.createElement("style");
    style.id = "pi-session-style";
    style.textContent = `
      .pi-mrmd {
        --rat-code-cell-padding-x: 14px;
        --rat-code-cell-gap-y: 0.75em;
        --rat-output-inset-left: var(--rat-code-cell-padding-x);
        --rat-output-padding-x: 14px;
        --editor-background: var(--vscode-editor-background);
        --editor-foreground: var(--vscode-editor-foreground);
        --editor-cursor: var(--vscode-editorCursor-foreground);
        --editor-selection: var(--vscode-editor-selectionBackground);
        --editor-active-line: var(--vscode-editor-lineHighlightBackground, transparent);
        --widget-font-mono: var(--vscode-editor-font-family, 'SF Mono', Consolas, monospace);
        --widget-font-sans: var(--vscode-font-family, -apple-system, BlinkMacSystemFont, 'Segoe WPC', 'Segoe UI', system-ui, sans-serif);
        --editor-font-family: var(--vscode-font-family, -apple-system, BlinkMacSystemFont, 'Segoe WPC', 'Segoe UI', system-ui, sans-serif);
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
        --mrmd-popup-bg: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)));
        --mrmd-hover-bg: var(--vscode-list-hoverBackground, var(--widget-surface-hover));
        --mrmd-selection-bg: var(--vscode-editor-selectionBackground);
        --mrmd-accent: var(--vscode-focusBorder, var(--vscode-textLink-foreground));
        --syntax-keyword: var(--vscode-symbolIcon-keywordForeground, var(--vscode-charts-purple, var(--vscode-editor-foreground)));
        --syntax-control: var(--vscode-symbolIcon-keywordForeground, var(--vscode-charts-purple, var(--vscode-editor-foreground)));
        --syntax-string: var(--vscode-symbolIcon-stringForeground, var(--vscode-charts-green, var(--vscode-editor-foreground)));
        --syntax-number: var(--vscode-symbolIcon-numberForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)));
        --syntax-comment: var(--vscode-editorCodeLens-foreground, var(--vscode-descriptionForeground));
        --syntax-function: var(--vscode-symbolIcon-functionForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)));
        --syntax-variable: var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground));
        --syntax-variable-special: var(--vscode-symbolIcon-variableForeground, var(--vscode-editor-foreground));
        --syntax-property: var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground));
        --syntax-operator: var(--vscode-symbolIcon-operatorForeground, var(--vscode-editor-foreground));
        --syntax-punctuation: var(--vscode-editor-foreground);
        --syntax-type: var(--vscode-symbolIcon-classForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)));
        --syntax-class: var(--vscode-symbolIcon-classForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)));
        --syntax-constant: var(--vscode-symbolIcon-constantForeground, var(--vscode-charts-orange, var(--vscode-editor-foreground)));
        --syntax-tag: var(--vscode-symbolIcon-structForeground, var(--vscode-charts-blue, var(--vscode-editor-foreground)));
        --syntax-attribute: var(--vscode-symbolIcon-propertyForeground, var(--vscode-editor-foreground));
        --syntax-meta: var(--vscode-descriptionForeground);
        --md-heading-color: var(--vscode-editor-foreground);
        --md-link-color: var(--vscode-textLink-foreground);
        --md-code-background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent));
        --md-code-color: var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground));
        --md-blockquote-border: var(--vscode-textBlockQuote-border, var(--vscode-focusBorder));
        --md-blockquote-color: var(--vscode-descriptionForeground);
        --md-marker-color: var(--vscode-descriptionForeground);
        --md-hr-color: var(--vscode-panel-border, var(--widget-border));
      }
      .pi-mrmd .cm-editor,
      .pi-mrmd .cm-scroller { background: transparent !important; color: var(--vscode-editor-foreground) !important; }
      .pi-mrmd .cm-content,
      .pi-mrmd .cm-line { color: var(--vscode-editor-foreground); }
      .pi-mrmd .cm-content { font-family: var(--vscode-font-family); font-size: var(--vscode-editor-font-size); }
      .card .pi-mrmd .cm-content { padding: 10px 12px 12px; }
      #compose.pi-mrmd .cm-content { padding: 8px 10px 12px; }
      .pi-mrmd .cm-md-inline-code { background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 16%, transparent)) !important; padding: 0.12em 0.32em !important; border-radius: 3px !important; }
      .pi-mrmd details {
        margin: 6px 0 10px var(--rat-output-inset-left, 14px);
        color: var(--vscode-editor-foreground);
      }
      .pi-mrmd details > summary {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        min-height: 20px;
        padding: 1px 8px 1px 6px;
        color: var(--vscode-descriptionForeground);
        background: var(--vscode-button-secondaryBackground, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent));
        border: 1px solid var(--vscode-button-border, var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent)));
        border-radius: 3px;
        font-family: var(--vscode-font-family);
        font-size: 11px;
        line-height: 18px;
        cursor: pointer;
        user-select: none;
      }
      .pi-mrmd details > summary:hover {
        color: var(--vscode-button-secondaryForeground, var(--vscode-foreground));
        background: var(--vscode-button-secondaryHoverBackground, var(--vscode-list-hoverBackground, color-mix(in srgb, var(--vscode-editor-foreground) 9%, transparent)));
      }
      .pi-mrmd details > summary:focus-visible {
        outline: 1px solid var(--vscode-focusBorder);
        outline-offset: 2px;
      }
      .pi-mrmd details > summary::marker { content: ""; }
      .pi-mrmd details > summary::-webkit-details-marker { display: none; }
      .pi-mrmd details > summary::before {
        content: "▸";
        color: var(--vscode-descriptionForeground);
        font-size: 10px;
      }
      .pi-mrmd details[open] > summary::before { content: "▾"; }

      .pi-mrmd .cm-codeblock,
      .pi-mrmd .cm-output,
      .pi-mrmd .cm-output-widget,
      .pi-mrmd .cm-cell-output,
      .pi-mrmd .mrmd-output { font-size: calc(var(--vscode-editor-font-size, 14px) * 0.92); }

      .pi-mrmd .cm-codeblock-line .cm-dt-keyword,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-keyword { color: var(--syntax-keyword) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-control-keyword,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-control-keyword { color: var(--syntax-control) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-string,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-string,
      .pi-mrmd .cm-codeblock-line .cm-dt-regexp,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-regexp,
      .pi-mrmd .cm-codeblock-line .cm-dt-escape,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-escape,
      .pi-mrmd .cm-codeblock-line .cm-dt-attribute-value,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-attribute-value { color: var(--syntax-string) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-number,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-number { color: var(--syntax-number) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-comment,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-comment { color: var(--syntax-comment) !important; font-style: italic !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-function,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-function { color: var(--syntax-function) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-variable,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-variable { color: var(--syntax-variable) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-type,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-type { color: var(--syntax-type) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-operator,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-operator { color: var(--syntax-operator) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-punctuation,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-punctuation { color: var(--syntax-punctuation) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-property,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-property,
      .pi-mrmd .cm-codeblock-line .cm-dt-attribute,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-attribute { color: var(--syntax-property) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-constant,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-constant { color: var(--syntax-constant) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-tag,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-tag { color: var(--syntax-tag) !important; }
      .pi-mrmd .cm-codeblock-line .cm-dt-meta,
      .pi-mrmd .cm-wysiwyg-code-fence-line .cm-dt-meta { color: var(--syntax-meta) !important; }

      .pi-mrmd .cm-codeblock-line,
      .pi-mrmd .cm-codeblock-fence,
      .pi-mrmd .cm-wysiwyg-code-fence-line { box-shadow: inset 0 0 0 9999px var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 12%, transparent)) !important; }

      .pi-mrmd .cm-codeblock-line,
      .pi-mrmd .cm-wysiwyg-code-fence-line:not(.cm-wysiwyg-code-fence-start):not(.cm-wysiwyg-code-fence-end) {
        padding-left: var(--rat-code-cell-padding-x, 14px);
        padding-right: var(--rat-code-cell-padding-x, 14px);
        line-height: 1.45;
      }

      .pi-mrmd .cm-codeblock-fence,
      .pi-mrmd .cm-codeblock-line,
      .pi-mrmd .cm-wysiwyg-code-fence-start,
      .pi-mrmd .cm-wysiwyg-code-fence-line,
      .pi-mrmd .cm-wysiwyg-code-fence-end {
        border-left: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent)));
        border-right: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent)));
      }

      .pi-mrmd .cm-codeblock-fence-open:not(.cm-output-fence-line):not(.cm-output-fence-editing) {
        position: relative;
        min-height: 1.85em !important;
        line-height: 1.85 !important;
        overflow: hidden !important;
        padding-right: 112px;
        border-top: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent))) !important;
        border-bottom: 0 !important;
        border-radius: 3px 3px 0 0;
      }
      .pi-mrmd .cm-codeblock-fence-open:not(.cm-output-fence-line):not(.cm-output-fence-editing) .cm-cell-controls {
        position: absolute;
        right: 8px;
        top: 50%;
        transform: translateY(-50%);
        margin-left: 0;
      }
      .pi-mrmd .cm-codeblock-fence-close:not(.cm-output-fence-line):not(.cm-output-fence-editing) {
        min-height: 1.85em !important;
        line-height: 1.85 !important;
        overflow: hidden !important;
        border-top: 0 !important;
        border-bottom: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent))) !important;
        border-radius: 0 0 3px 3px;
      }
      .pi-mrmd .cm-codeblock-fence:not(.cm-activeLine):not(.cm-output-fence-line):not(.cm-output-fence-editing) { color: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 6%, transparent)) !important; }
      .pi-mrmd .cm-codeblock-fence.cm-activeLine:not(.cm-output-fence-line):not(.cm-output-fence-editing) { color: var(--vscode-descriptionForeground) !important; }
      .pi-mrmd .cm-wysiwyg-code-header { min-height: 0; height: 6px; padding: 0; border-bottom: 0 !important; background: transparent !important; overflow: hidden; }
      .pi-mrmd .cm-wysiwyg-code-header-lang { padding: 0 4px; text-transform: none; letter-spacing: 0; font-weight: 400; font-size: 11px; }
      .pi-mrmd .cm-wysiwyg-code-header-btn { width: 20px; height: 20px; opacity: 0.45; }
      .pi-mrmd .cm-wysiwyg-code-header:hover .cm-wysiwyg-code-header-btn { opacity: 1; }
      .pi-mrmd .cm-wysiwyg-code-fence-start .cm-wysiwyg-code-header-lang,
      .pi-mrmd .cm-wysiwyg-code-fence-start .cm-wysiwyg-code-header-btn { opacity: 0; }

      .pi-mrmd .cm-output-fence-line { height: 0 !important; min-height: 0 !important; line-height: 0 !important; font-size: 1px !important; overflow: hidden !important; padding: 0 !important; margin: 0 !important; border: 0 !important; box-shadow: none !important; background: transparent !important; color: transparent !important; }
      .pi-mrmd .cm-output-content-line:not(.cm-rich-output-hidden) {
        margin-left: var(--rat-output-inset-left, 14px) !important;
        margin-right: 0 !important;
        padding-left: var(--rat-output-padding-x, 14px) !important;
        padding-right: var(--rat-output-padding-x, 14px) !important;
        background: transparent !important;
        box-shadow: inset 0 0 0 9999px var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 12%, transparent)) !important;
        border-left: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent))) !important;
        border-right: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent))) !important;
        color: var(--vscode-descriptionForeground, var(--vscode-editor-foreground)) !important;
        opacity: 0.9;
      }
      .pi-mrmd .cm-output-fence-start + .cm-output-content-line:not(.cm-rich-output-hidden) { padding-top: 0.45em !important; border-top: 1px solid var(--vscode-editorWidget-border, var(--vscode-panel-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent))) !important; border-top-left-radius: 3px; border-top-right-radius: 3px; }
      .pi-mrmd .cm-output-content-line:not(.cm-rich-output-hidden):has(+ .cm-output-fence-end) { padding-bottom: 0.45em !important; border-bottom: var(--rat-code-cell-gap-y, 0.75em) solid transparent !important; border-bottom-left-radius: 3px; border-bottom-right-radius: 3px; }
      .pi-mrmd .cm-output-widget,
      .pi-mrmd .cm-empty-output-widget { left: var(--rat-output-inset-left, 14px) !important; right: 0 !important; }
      .pi-mrmd .cm-scroll-output-widget,
      .pi-mrmd .cm-json-output-widget,
      .pi-mrmd .cm-html-output-widget,
      .pi-mrmd .cm-css-output-widget { left: auto !important; margin-left: var(--rat-output-inset-left, 14px) !important; margin-right: 0 !important; }

      .pi-mrmd .cm-cursor, .pi-mrmd .cm-dropCursor { border-left-color: var(--vscode-editorCursor-foreground) !important; }
      .pi-mrmd .cm-selectionBackground, .pi-mrmd .cm-content ::selection { background: var(--vscode-editor-selectionBackground) !important; }
      .pi-mrmd .cm-activeLine { background: var(--vscode-editor-lineHighlightBackground, transparent) !important; }
      .card:not(.editable) .pi-mrmd .cm-focused { outline: none !important; }
      .card:not(.editable) .pi-mrmd .cm-activeLine { background: transparent !important; }
      .card:not(.editable) .pi-mrmd .cm-cursor,
      .card:not(.editable) .pi-mrmd .cm-dropCursor,
      .card:not(.editable) .pi-mrmd .cm-selectionMatch,
      .card:not(.editable) .pi-mrmd .cm-matchingBracket,
      .card:not(.editable) .pi-mrmd .cm-nonmatchingBracket { display: none !important; }
      .card:not(.editable) .pi-mrmd .cm-content { caret-color: transparent !important; cursor: text; }
      .card:not(.editable) .pi-mrmd .cm-line { cursor: text; }
      .pi-mrmd .cm-tooltip, .pi-mrmd .cm-panel { background: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background))) !important; color: var(--vscode-editor-foreground) !important; border-color: var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent)) !important; }
      .pi-mrmd .cm-frontmatter,
      .pi-mrmd .cm-frontmatter-card,
      .pi-mrmd .cm-frontmatter-abstract,
      .pi-mrmd .cm-frontmatter-keyword,
      .pi-mrmd .cm-output,
      .pi-mrmd .cm-output-widget,
      .pi-mrmd .cm-cell-output,
      .pi-mrmd .mrmd-output { background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 5%, transparent)); color: var(--vscode-editor-foreground); border-color: var(--vscode-editorWidget-border, var(--vscode-panel-border, transparent)); }
    `;
    document.head.appendChild(style);
  }

  function scheduleEdit(entryId, editor) {
    if (editTimers.has(entryId)) clearTimeout(editTimers.get(entryId));
    editTimers.set(entryId, setTimeout(() => {
      editTimers.delete(entryId);
      vscode.postMessage({ type: "editEntry", entryId, text: editor.getContent() });
    }, 300));
  }

  function editorOptions(entryId, role, text, interactive, extra) {
    return Object.assign({
      doc: text || "",
      javascript: false,
      runtimes: interactive ? { rat: createRatRuntime(entryId) } : {},
      placeholder: interactive ? "Edit session message…" : "Double-click to edit…",
      theme: hostThemeName(),
      documentTemplate: createHostDocumentTemplate(),
      documentStylePreview: true,
      documentPresentationMode: "flow",
      themingMode: "hosted",
      outputWidgets: false,
      sectionControls: false,
      userName: role,
      readonly: !interactive,
    }, extra || {});
  }

  function mountCardEditor(card, entry, text, interactive) {
    const previous = editors.get(entry.id);
    previous?.destroy?.();
    const mount = card.querySelector(".editor");
    mount.textContent = "";
    mount.classList.add("pi-mrmd");
    card.classList.toggle("editable", interactive);
    const save = card.querySelector(".save");
    if (save) save.textContent = interactive ? "Save" : "Edit";
    const editor = window.mrmd.create(mount, editorOptions(entry.id, entry.role, text, interactive, {
      projectRoot: entry.cwd || null,
      documentPath: entry.sessionPath || null,
    }));
    if (!interactive) makeReadOnly(editor);
    mount.addEventListener("mouseup", () => requestAnimationFrame(() => blurInactiveEditor(mount)));
    editors.set(entry.id, editor);
    return editor;
  }

  function deactivateActiveCard() {
    if (!activeEntryId) return;
    const card = root.querySelector(`[data-entry-id="${CSS.escape(activeEntryId)}"]`);
    const editor = editors.get(activeEntryId);
    if (card && editor) {
      const entry = card.__entry;
      const text = editor.getContent();
      vscode.postMessage({ type: "editEntry", entryId: activeEntryId, text });
      mountCardEditor(card, entry, text, false);
    }
    activeEntryId = null;
  }

  function isToolRole(role) {
    return String(role || "").startsWith("tool");
  }

  function activateCard(card) {
    const entry = card.__entry;
    if (!entry || isToolRole(entry.role) || activeEntryId === entry.id) return;
    deactivateActiveCard();
    const current = editors.get(entry.id);
    const text = current?.getContent?.() ?? entry.markdown ?? "";
    const editor = mountCardEditor(card, entry, text, true);
    activeEntryId = entry.id;
    editor.onChange(() => scheduleEdit(entry.id, editor));
    editor.focus?.();
    setStatus("Editing " + entry.role + " message");
  }

  function createCard(entry) {
    const card = document.createElement("section");
    card.className = "card role-" + entry.role;
    card.dataset.entryId = entry.id;
    card.__entry = entry;
    const isTool = isToolRole(entry.role);
    card.innerHTML = `
      <header>
        <span class="role-dot" title="${entry.role}"></span>
        ${isTool ? "" : `<button class="save" title="Edit message">Edit</button>`}
      </header>
      <div class="editor"></div>
    `;
    root.appendChild(card);

    mountCardEditor(card, entry, entry.markdown || "", false);
    if (isTool) return;
    card.addEventListener("dblclick", () => activateCard(card));
    card.querySelector(".save")?.addEventListener("click", (event) => {
      event.stopPropagation();
      if (activeEntryId !== entry.id) {
        activateCard(card);
        return;
      }
      const current = editors.get(entry.id);
      vscode.postMessage({ type: "editEntry", entryId: entry.id, text: current?.getContent?.() || "" });
    });
  }

  function createCompose() {
    if (!composeRoot || composeEditor || !window.mrmd?.create) return;
    composeRoot.classList.add("pi-mrmd");
    composeEditor = window.mrmd.create(composeRoot, {
      doc: composeDraft,
      javascript: false,
      runtimes: { rat: createRatRuntime("compose") },
      placeholder: "Write a new Pi request… Markdown and runnable Rat cells work here too.",
      theme: hostThemeName(),
      documentTemplate: createHostDocumentTemplate(),
      documentStylePreview: true,
      documentPresentationMode: "flow",
      themingMode: "hosted",
      outputWidgets: false,
      sectionControls: false,
      projectRoot: currentCwd || null,
      userName: "user",
    });
    composeEditor.onChange?.(() => {
      composeDraft = composeEditor?.getContent?.() || "";
      vscode.setState?.({ ...(vscode.getState?.() || {}), composeDraft });
    });
    composeRoot.addEventListener("focusin", () => { composeHadFocus = true; });
    composeRoot.addEventListener("focusout", () => { composeHadFocus = false; });
    composeRoot.addEventListener("keydown", (event) => {
      if (event.altKey && event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        sendCompose();
      }
    }, true);
    if (composeHadFocus) queueMicrotask(() => composeEditor?.focus?.());
  }

  const sessionContainer = document.getElementById("session-container");

  function upsertSystemPrompt(text, loaded) {
    currentSystemPrompt = String(text || "");
    let card = root.querySelector(".system-prompt-card");
    if (!card) {
      card = document.createElement("section");
      card.className = "card role-system system-prompt-card";
      card.innerHTML = `
        <details>
          <summary>System prompt <span class="system-prompt-hint">click to reconstruct current prompt</span></summary>
          <pre></pre>
        </details>
      `;
      const details = card.querySelector("details");
      details.addEventListener("toggle", () => {
        if (!details.open || card.dataset.loading === "true") return;
        void loadSystemPrompt(card);
      });
      root.prepend(card);
    }
    if (loaded) card.dataset.loaded = "true";
    card.dataset.loading = "false";
    card.querySelector("pre").textContent = currentSystemPrompt || "Open this block to reconstruct the system prompt that would be sent now.";
  }

  async function loadSystemPrompt(card) {
    card.dataset.loading = "true";
    card.querySelector("pre").textContent = "Reconstructing system prompt…";
    try {
      const result = await rpc("getSystemPrompt", { text: composeEditor?.getContent?.() || composeDraft || "" }, 30_000);
      upsertSystemPrompt(result?.text || "", true);
    } catch (error) {
      card.dataset.loading = "false";
      card.querySelector("pre").textContent = error.message || "Could not reconstruct system prompt.";
    }
  }

  function render(entries, title) {
    installThemeStyle();
    showEditor(title);
    editors.forEach((editor) => editor.destroy?.());
    editors.clear();
    activeEntryId = null;
    currentSystemPrompt = "";
    root.textContent = "";
    upsertSystemPrompt(currentSystemPrompt, false);
    for (const entry of entries) createCard(entry);
    createCompose();
    setStatus(entries.length ? `${entries.length} messages` : "ready");
    setTimeout(() => {
      if (sessionContainer) sessionContainer.scrollTop = sessionContainer.scrollHeight;
    }, 100);
  }

  function appendLiveCard(role, id, initialText) {
    const card = document.createElement("section");
    card.className = "card role-" + role;
    card.dataset.entryId = id;
    card.innerHTML = `
      <header>
        <span class="role-dot" title="${role}"></span>
      </header>
      <div class="editor"></div>
    `;
    root.appendChild(card);
    const mount = card.querySelector(".editor");
    mount.classList.add("pi-mrmd");
    const editor = window.mrmd.create(mount, editorOptions(id, role, initialText || "", false, {
      placeholder: "Streaming…",
    }));
    makeReadOnly(editor);
    mount.addEventListener("mouseup", () => requestAnimationFrame(() => blurInactiveEditor(mount)));
    editors.set(id, editor);
    if (sessionContainer) sessionContainer.scrollTop = sessionContainer.scrollHeight;
    return editor;
  }

  function sendCompose() {
    if (!composeEditor) return;
    const text = composeEditor.getContent();
    if (!text.trim()) return;
    composeDraft = "";
    vscode.setState?.({ ...(vscode.getState?.() || {}), composeDraft });
    appendLiveCard("user", "pending-user", text);
    streamingText = "";
    streamingEditor = appendLiveCard("assistant", "streaming-assistant", "");
    composeEditor.setContent("");
    isPiRunning = true;
    if (treeButton) treeButton.disabled = true;
    updateControlsUi(controlsState);
    vscode.postMessage({ type: "sendUserMessage", text, mode: currentMode });
    setStatus("Sending to Pi…");
  }

  function completeRun(message) {
    const run = pendingRuns.get(message.id);
    if (!run) return;
    pendingRuns.delete(message.id);
    const parsed = splitRatAssets(message.stdout || "");
    if (parsed.assets.length && typeof run.onAsset === "function") {
      for (const assetUrl of parsed.assets) {
        run.onAsset({ url: assetUrl, mimeType: guessMime(assetUrl), assetType: "image" }, "image");
      }
    }
    const stdout = parsed.text;
    let delta = stdout.startsWith(run.accumulated) ? stdout.slice(run.accumulated.length) : stdout;
    try {
      run.onChunk(delta, stdout, true);
      run.resolve({ success: !!message.success, stdout, stderr: message.stderr || "", error: message.error || null });
      setStatus(message.success ? "Done" : "Finished with errors");
    } catch (error) {
      run.reject(error);
    }
  }

  window.addEventListener("message", (event) => {
    const message = event.data || {};
    switch (message.type) {
      case "init":
        currentCwd = message.cwd || null;
        setModeOptions(message.modes || []);
        updateSessionNameUi(message.sessionName || "", true);
        updateModeUi(message.mode || modeOptions[0]?.value || "", true);
        if (composeEditor) {
          composeDraft = composeEditor.getContent?.() || composeDraft;
          composeHadFocus = !!composeRoot.querySelector(".cm-focused");
          vscode.setState?.({ ...(vscode.getState?.() || {}), composeDraft });
        }
        render(message.entries || [], message.title);
        if (composeHadFocus) queueMicrotask(() => composeEditor?.focus?.());
        break;
      case "sessions":
        allSessions = message.sessions || [];
        sessionScope = message.scope || "workspace";
        sessionLimit = Number(message.limit) || 200;
        if (allSessionsButton) {
          allSessionsButton.textContent = sessionScope === "all" ? "Workspace" : "All";
          allSessionsButton.title = sessionScope === "all" ? "Show current workspace sessions" : "Show recent sessions across all workspaces";
        }
        renderSessions();
        break;
      case "saved": setStatus("Saved"); break;
      case "sessionNameChanged":
        updateSessionNameUi(message.sessionName || "", true);
        setStatus("Session name saved");
        break;
      case "modeChanged":
        updateModeUi(message.mode || currentMode, true);
        root.querySelector(".system-prompt-card")?.removeAttribute("data-loaded");
        setStatus("Mode: " + modeLabel(currentMode));
        break;
      case "piSystemPrompt":
        upsertSystemPrompt(message.text || "", true);
        break;
      case "ratOutput": {
        const run = pendingRuns.get(message.id);
        if (!run) return;
        const chunk = String(message.chunk || "");
        run.accumulated += chunk;
        run.onChunk(chunk, run.accumulated, false);
        break;
      }
      case "ratDone": completeRun(message); break;
      case "ratError": {
        const run = pendingRuns.get(message.id);
        if (!run) return;
        pendingRuns.delete(message.id);
        run.reject(new Error(message.error || "rat execution failed"));
        break;
      }
      case "rpcResult": resolveRpc(message, true); break;
      case "rpcError": resolveRpc(message, false); break;
      case "piAssistantStart":
        if (!streamingEditor || streamingText.trim()) {
          streamingText = "";
          streamingEditor = appendLiveCard("assistant", message.id || `assistant-${Date.now()}`, "");
        }
        setStatus("Pi is responding…");
        break;
      case "piStream":
        if (typeof message.text === "string") {
          streamingText = message.text;
        } else if (typeof message.delta === "string") {
          streamingText += message.delta;
        }
        if (streamingEditor) {
          streamingEditor.setContent(streamingText);
          if (sessionContainer) sessionContainer.scrollTop = sessionContainer.scrollHeight;
        }
        setStatus("Pi is responding…");
        break;
      case "piToolStart": {
        const id = `tool-${message.toolCallId}`;
        const args = JSON.stringify({ toolCallId: message.toolCallId, name: message.toolName || "tool", arguments: message.args || {} }, null, 2);
        const markdown = `**${message.toolName || "tool"}**\n\n${limitedCodeFence(args, "json", "head", "Show full tool call")}`;
        appendLiveCard("tool", id, markdown);
        setStatus(`Pi tool: ${message.toolName || "tool"}`);
        break;
      }
      case "piToolUpdate": {
        const id = `tool-${message.toolCallId}`;
        let editor = editors.get(id);
        if (!editor) editor = appendLiveCard("tool", id, `**${message.toolName || "tool"}**`);
        editor.setContent(message.markdown || "Working…");
        if (sessionContainer) sessionContainer.scrollTop = sessionContainer.scrollHeight;
        break;
      }
      case "piToolEnd": {
        const id = `tool-${message.toolCallId}`;
        let editor = editors.get(id);
        if (!editor) editor = appendLiveCard("tool", id, "");
        editor.setContent(message.markdown || "Done");
        if (sessionContainer) sessionContainer.scrollTop = sessionContainer.scrollHeight;
        break;
      }
      case "piStatus":
        setStatus(message.text || "Pi is running…");
        break;
      case "piDone":
        isPiRunning = false;
        if (treeButton) treeButton.disabled = !editorView.classList.contains("active");
        updateControlsUi(controlsState);
        setStatus(message.success ? "Pi response complete" : "Pi failed");
        if (message.reload) {
          setTimeout(() => vscode.postMessage({ type: "reload" }), 500);
        }
        break;
      case "authFlow":
        if (message.event === "auth") {
          authDetails.innerHTML = `<h3>Complete browser login</h3><p class="muted"></p><p></p>`;
          authDetails.querySelector(".muted").textContent = message.instructions || "A browser window was opened. Complete login there.";
          const link = document.createElement("a");
          link.href = message.url;
          link.textContent = message.url;
          authDetails.querySelector("p:last-child").appendChild(link);
        } else if (message.event === "progress") {
          setStatus(message.text || "Login in progress…");
        } else if (message.event === "prompt") {
          pendingAuthPromptId = message.promptId;
          authPromptBox?.classList.remove("hidden");
          authPromptText.textContent = message.prompt || "Input required";
          authPromptInput.value = "";
          authPromptInput.placeholder = message.placeholder || "";
          authPromptInput.focus();
        }
        break;
      case "showError": setStatus(message.message || "Error"); break;
    }
  });

  reloadButton?.addEventListener("click", () => vscode.postMessage({ type: "reload", scope: sessionScope }));
  treeButton?.addEventListener("click", openTreeDialog);
  modelButton?.addEventListener("click", () => openModelDialog().catch((error) => setStatus(error.message || "Could not open model picker")));
  thinkingButton?.addEventListener("click", openThinkingDialog);
  authButton?.addEventListener("click", () => openAuthDialog().catch((error) => setStatus(error.message || "Could not open auth")));
  treeCloseButton?.addEventListener("click", closeTreeDialog);
  treeDialog?.addEventListener("click", (event) => {
    if (event.target === treeDialog) closeTreeDialog();
  });
  treeSearchInput?.addEventListener("input", renderTreeView);
  treeFilterSelect?.addEventListener("change", renderTreeView);
  treeSummarySelect?.addEventListener("change", () => {
    if (treeCustomSummaryInput) treeCustomSummaryInput.disabled = treeSummarySelect.value !== "custom";
  });
  treeContinueButton?.addEventListener("click", () => navigateSelectedTreeNode().catch((error) => setStatus(error.message || "Tree navigation failed")));
  treeSaveLabelButton?.addEventListener("click", () => saveTreeLabel(false).catch((error) => setStatus(error.message || "Could not save label")));
  treeClearLabelButton?.addEventListener("click", () => saveTreeLabel(true).catch((error) => setStatus(error.message || "Could not clear label")));
  modelCloseButton?.addEventListener("click", closeModelDialog);
  modelDialog?.addEventListener("click", (event) => { if (event.target === modelDialog) closeModelDialog(); });
  modelSearchInput?.addEventListener("input", renderModelList);
  thinkingCloseButton?.addEventListener("click", closeThinkingDialog);
  thinkingDialog?.addEventListener("click", (event) => { if (event.target === thinkingDialog) closeThinkingDialog(); });
  authCloseButton?.addEventListener("click", closeAuthDialog);
  authDialog?.addEventListener("click", (event) => { if (event.target === authDialog) closeAuthDialog(); });
  authSearchInput?.addEventListener("input", renderAuthList);
  authSaveKeyButton?.addEventListener("click", () => saveApiKey().catch((error) => setStatus(error.message || "Could not save API key")));
  authLoginOAuthButton?.addEventListener("click", () => loginOAuth().catch((error) => setStatus(error.message || "Login failed")));
  authLogoutButton?.addEventListener("click", () => logoutProvider().catch((error) => setStatus(error.message || "Logout failed")));
  authPromptSubmit?.addEventListener("click", () => {
    if (!pendingAuthPromptId) return;
    vscode.postMessage({ type: "authPromptResponse", promptId: pendingAuthPromptId, value: authPromptInput.value });
    pendingAuthPromptId = null;
    authPromptBox?.classList.add("hidden");
  });
  authPromptInput?.addEventListener("keydown", (event) => {
    if (event.key === "Enter") authPromptSubmit?.click();
  });
  newSessionButton?.addEventListener("click", () => {
    setStatus("Creating Pi session…");
    vscode.postMessage({ type: "newSession" });
  });
  allSessionsButton?.addEventListener("click", () => {
    if (sessionScope === "all") {
      sessionScope = "workspace";
      setStatus("Loading workspace sessions…");
      vscode.postMessage({ type: "refresh", scope: "workspace" });
    } else {
      sessionScope = "all";
      setStatus("Scanning recent sessions…");
      vscode.postMessage({ type: "loadAllSessions" });
    }
  });
  sendButton?.addEventListener("click", () => {
    sendCompose();
  });
  sessionNameInput?.addEventListener("focus", () => {
    if (sessionNameInput.value === "Untitled" && !currentSessionName) sessionNameInput.select();
  });
  sessionNameInput?.addEventListener("blur", saveSessionName);
  sessionNameInput?.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      saveSessionName();
      sessionNameInput.blur();
    } else if (event.key === "Escape") {
      event.preventDefault();
      updateSessionNameUi(currentSessionName, true);
      sessionNameInput.blur();
    }
  });
  modeSelect?.addEventListener("change", async () => {
    const mode = modeSelect.value;
    if (mode === ADD_MODE_VALUE) {
      openModeDialog({ key: "", label: "", opener: "", appendix: "" }, false);
      return;
    }
    if (mode === EDIT_MODE_VALUE) {
      try {
        const definition = await rpc("getModeDefinition", { mode: currentMode }, 10_000);
        openModeDialog(definition, true);
      } catch (error) {
        setStatus(error.message || "Could not load mode");
        updateModeUi(currentMode, true);
      }
      return;
    }
    if (!modeOptions.some((option) => option.value === mode)) return;
    currentMode = mode;
    root.querySelector(".system-prompt-card")?.removeAttribute("data-loaded");
    setStatus("Mode: " + modeLabel(mode));
    vscode.postMessage({ type: "setMode", mode });
  });
  modeCancelButton?.addEventListener("click", closeModeDialog);
  modeDialog?.addEventListener("click", (event) => {
    if (event.target === modeDialog) closeModeDialog();
  });
  modeLabelInput?.addEventListener("input", () => {
    if (!modeKeyInput.disabled && !modeKeyInput.value.trim()) modeKeyInput.value = modeKeyFromLabel(modeLabelInput.value);
  });
  modeForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    saveModeFromDialog().catch((error) => setStatus(error.message || "Could not save mode"));
  });
  goHomeButton?.addEventListener("click", showHome);
  searchInput?.addEventListener("input", renderSessions);
  document.addEventListener("pointerdown", (event) => {
    if (!activeEntryId) return;
    const activeCard = root.querySelector(`[data-entry-id="${CSS.escape(activeEntryId)}"]`);
    if (activeCard?.contains(event.target)) return;
    deactivateActiveCard();
  }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    if (treeDialog && !treeDialog.classList.contains("hidden")) {
      event.preventDefault();
      closeTreeDialog();
      return;
    }
    if (modelDialog && !modelDialog.classList.contains("hidden")) { event.preventDefault(); closeModelDialog(); return; }
    if (thinkingDialog && !thinkingDialog.classList.contains("hidden")) { event.preventDefault(); closeThinkingDialog(); return; }
    if (authDialog && !authDialog.classList.contains("hidden")) { event.preventDefault(); closeAuthDialog(); return; }
    const active = document.activeElement;
    const focusedEditor = active?.closest?.(".pi-mrmd");
    if (!focusedEditor && !activeEntryId) return;
    event.preventDefault();
    event.stopPropagation();
    deactivateActiveCard();
    if (active instanceof HTMLElement) active.blur();
    focusedEditor?.querySelector(".cm-content")?.blur?.();
    focusedEditor?.querySelector(".cm-editor")?.classList.remove("cm-focused");
  }, true);
  const themeObserver = new MutationObserver(() => {
    syncAllEditorThemes();
    requestAnimationFrame(syncAllEditorThemes);
  });
  themeObserver.observe(document.body, { attributes: true, attributeFilter: ["class", "style"] });
  vscode.postMessage({ type: "ready" });
})();
