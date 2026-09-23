/**
 * rat-notebook — what a Markdown notebook run on rat means, as plain
 * functions: no editor, no DOM, no transport. Every host (Chattering, the
 * VS Code extension, a test) uses these, so a notebook behaves the same
 * wherever it is run.
 *
 * The result format a run leaves in the document:
 *
 *   ```python
 *   plt.plot(x); plt.show()
 *   ```
 *
 *   ```output
 *   what the program printed
 *   ```
 *
 *   ![plot](../_assets/generated/3f9a1c2b7d4e.png)
 *
 * - Program output only: no timing or status (they change on every run and
 *   would show in Git when the output did not). A fence longer than any
 *   backtick run in the output keeps the output byte-for-byte.
 * - Plots are images after the block, saved in the project's
 *   `_assets/generated/`, named by content. A result owns only the images
 *   the runner made (alt `plot`, a path inside `_assets/`): an image a
 *   person placed there is never replaced.
 * - Older forms are read and replaced: ```output:<execId> (MRMD) and
 *   ```output | ✓ 1.5s | 1 var (VS Code before this module).
 */

/** Where generated images go, relative to the project root. */
export const GENERATED_ASSETS_DIR = '_assets/generated';

const PLOT_MARKER = '__RAT_PLOT__:';
const PLOT_LINE = /^__RAT_PLOT__:(.+?)\s*$/;
const BANNER_LINE = /^[a-z0-9@._-]+ (?:started|restarted) on http[^\n]*\n?/im;
const STATUS_TAIL = /\n?[✓✗] \d+(?:\.\d+)?m?s( \| \d+ vars?)?\s*$/;
const OWNED_IMAGE = /^!\[plot(?:-\d+)?\]\(([^)\s]*_assets\/[^)\s]*)\)\s*$/;

/** A fence line's language word, lowercased ('' when bare). */
export function fenceLanguage(line) {
  return ((String(line).match(/^\s{0,3}(?:`{3,}|~{3,})\s*([^\s|]*)/) || [])[1] || '').toLowerCase();
}

/** True for every spelling of a result fence: output, output:<id>, output | … */
export function isOutputFence(line) {
  const lang = fenceLanguage(line);
  return lang === 'output' || lang.startsWith('output:');
}

/** True for an image line a run made (and a rerun may replace). */
export function isOwnedImageLine(line) {
  return OWNED_IMAGE.test(String(line));
}

/**
 * rat's final text for a run, as a document keeps it: without rat's
 * kernel-start banner and its "✓ 21ms | 1 var" status line.
 */
export function cleanRunOutput(out) {
  return String(out || '').replace(BANNER_LINE, '').replace(STATUS_TAIL, '').replace(/\s+$/, '');
}

/** Split plot markers out of finished output: {text, plots: [path]}. */
export function splitPlots(text) {
  const plots = [];
  const kept = [];
  for (const line of String(text || '').split('\n')) {
    const m = line.match(PLOT_LINE);
    if (m) plots.push(m[1]);
    else kept.push(line);
  }
  return { text: kept.join('\n').replace(/\s+$/, ''), plots };
}

/**
 * The same, for output that arrives in chunks: a marker may be split
 * across chunks, so a partial last line is held back — but only while it
 * could still become a marker (a prompt like "Name: " passes at once).
 * feed(chunk) → {text, plots}; flush() → the rest.
 */
export function createLiveOutputFilter() {
  let pending = '';
  const take = (final) => {
    const out = { text: '', plots: [] };
    let start = 0;
    for (;;) {
      const nl = pending.indexOf('\n', start);
      if (nl < 0) break;
      const line = pending.slice(start, nl);
      const m = line.match(PLOT_LINE);
      if (m) out.plots.push(m[1]);
      else out.text += line + '\n';
      start = nl + 1;
    }
    let rest = pending.slice(start);
    if (rest && (final || !PLOT_MARKER.startsWith(rest.slice(0, PLOT_MARKER.length)))) {
      const m = final && rest.match(PLOT_LINE);
      if (m) out.plots.push(m[1]);
      else out.text += rest;
      rest = '';
    }
    pending = rest;
    return out;
  };
  return {
    feed(chunk) { pending += String(chunk ?? ''); return take(false); },
    flush() { return take(true); },
  };
}

/** Backticks for a fence around `text`: longer than any run inside it. */
export function fenceFor(text) {
  let longest = 0;
  for (const m of String(text).matchAll(/`+/g)) longest = Math.max(longest, m[0].length);
  return '`'.repeat(Math.max(3, longest + 1));
}

/**
 * The Markdown a run leaves under its cell: the output block (when there
 * is output) and the plot images (each `{src, alt}`), or '' for neither.
 */
export function formatResult(text, images = []) {
  const body = String(text ?? '').replace(/\s+$/, '');
  const parts = [];
  if (body) {
    const ticks = fenceFor(body);
    parts.push(ticks + 'output\n' + body + '\n' + ticks);
  }
  for (const img of images) parts.push('![' + (img.alt || 'plot') + '](' + img.src + ')');
  return parts.join('\n\n');
}

/**
 * The run's output as a finished document keeps it, given what rat
 * reported at the end: {text, plots}. `ok`=false keeps the error text.
 */
export function finishedOutput(out) {
  return splitPlots(cleanRunOutput(out));
}

/**
 * Which cell another client's run belongs to: the only cell whose code is
 * the run's code (trailing whitespace aside). Two identical cells, or
 * none: null — a guess would draw someone's run on the wrong cell.
 */
export function cellForCode(cells, code) {
  const norm = s => String(s ?? '').replace(/\s+$/, '').replace(/\r\n/g, '\n');
  const want = norm(code);
  if (!want) return null;
  const hits = cells.filter(c => norm(c.code) === want);
  return hits.length === 1 ? hits[0] : null;
}

/**
 * Follows other clients' runs from `rat events` (run_started/output/
 * waiting/input_done/ended, as parsed JSON). Live chunks are a preview
 * sent every 50 ms — a quick run has none — so the end event's whole
 * output fills in what the chunks did not. Returns per event what changed:
 * {run, kind, text?, plots?} — the host draws it.
 */
export function createRunFollower() {
  const runs = new Map();
  return {
    runs,
    apply(ev) {
      const kind = ev.event || ev.kind;
      const id = ev.run_id;
      if (!id) return { kind, run: null };
      if (kind === 'run_started') {
        const run = { id, caller: ev.caller || 'rat', code: ev.code || '', startedAt: Date.now(), seen: '', filter: createLiveOutputFilter(), waiting: null, replay: !!ev.replay };
        runs.set(id, run);
        return { kind, run };
      }
      const run = runs.get(id);
      if (!run) return { kind, run: null };
      if (kind === 'run_output') {
        run.seen += ev.text || '';
        return { kind, run, ...run.filter.feed(ev.text || '') };
      }
      if (kind === 'run_waiting') { run.waiting = { prompt: ev.prompt || '', secret: !!ev.secret }; return { kind, run }; }
      if (kind === 'run_input_done') { run.waiting = null; return { kind, run }; }
      if (kind === 'run_ended') {
        runs.delete(id);
        run.waiting = null;
        const tail = run.filter.flush();
        const full = ev.ok === false && ev.error ? String(ev.error) : String(ev.output || '');
        const seen = run.seen.replace(/\s+$/, '');
        let rest = '';
        if (!seen) rest = full;
        else if (full.startsWith(seen)) rest = full.slice(seen.length).replace(/^\n/, '');
        const more = splitPlots(rest);
        run.ok = ev.ok !== false;
        run.ms = typeof ev.duration_ms === 'number' ? ev.duration_ms : Date.now() - run.startedAt;
        return { kind, run, text: tail.text + (more.text ? (tail.text && !tail.text.endsWith('\n') ? '\n' : '') + more.text + '\n' : ''), plots: [...tail.plots, ...more.plots] };
      }
      return { kind, run };
    },
  };
}
