/**
 * installRat.ts — first-use installer for the rat CLI.
 *
 * VS Code extensions do not get a reliable "post-install" hook where they can
 * run arbitrary setup. Instead, we make the CLI self-serve on first use:
 * when a rat command is missing, ask the user, download the platform binary
 * into extension storage, then mirror it into the user's normal PATH so `rat`
 * works from terminals too.
 */

import * as cp from "child_process";
import * as crypto from "crypto";
import * as fs from "fs";
import * as https from "https";
import * as path from "path";
import * as vscode from "vscode";

let context: vscode.ExtensionContext | null = null;
let installPromise: Promise<string> | null = null;

const DOWNLOAD_BASE_URL =
  process.env.RAT_VSCODE_DOWNLOAD_BASE_URL ??
  "https://github.com/maximerivest/rat/releases/latest/download";

const CHECKSUM_BASE_URL =
  process.env.RAT_VSCODE_CHECKSUM_BASE_URL ?? DOWNLOAD_BASE_URL;

const INSTALL_TIMEOUT_MS = 120_000;
const VERIFY_TIMEOUT_MS = 10_000;

export function initRatInstaller(ctx: vscode.ExtensionContext): void {
  context = ctx;
}

/** Return the extension-managed rat binary path. */
export function managedRatPath(): string | null {
  if (!context) return null;
  const exe = ratExecutableName();
  return path.join(context.globalStorageUri.fsPath, "bin", exe);
}

/** Return the path where the extension mirrors rat for normal terminals. */
export function userPathRatPath(): string | null {
  const dir = userPathInstallDir();
  return dir ? path.join(dir, ratExecutableName()) : null;
}

/**
 * Resolve the binary the extension should execute.
 *
 * Priority:
 * 1. User setting rat.path when explicitly set to something other than "rat".
 * 2. Extension-installed user PATH binary, if present.
 * 3. Extension-managed fallback binary, if present.
 * 4. Plain "rat" on PATH.
 */
export function resolveRatExecutable(): string {
  const configured = vscode.workspace
    .getConfiguration("rat")
    .get<string>("path", "rat")
    .trim();

  if (configured && configured !== "rat") return configured;

  const userPath = userPathRatPath();
  if (userPath && fs.existsSync(userPath)) return userPath;

  const managed = managedRatPath();
  if (managed && fs.existsSync(managed)) return managed;

  return configured || "rat";
}

/** Build a shell-safe command for the VS Code integrated terminal. */
export function ratTerminalCommand(args: string[]): string {
  return [shellQuote(resolveRatExecutable()), ...args.map(shellQuote)].join(" ");
}

/** Command-palette entry: Rat: Install CLI. */
export async function installRatCliCommand(): Promise<void> {
  const installed = await ensureRatInstalled({ manual: true, force: true });
  vscode.window.showInformationMessage(`Rat CLI installed/updated: ${installed}`);
}

/**
 * Ensure a working rat CLI is available. Used after execFile reports ENOENT.
 * The user is always asked before anything is downloaded.
 */
export async function ensureRatInstalled(
  opts: { manual?: boolean; force?: boolean } = {},
): Promise<string> {
  if (installPromise) return installPromise;

  installPromise = doEnsureRatInstalled(opts).finally(() => {
    installPromise = null;
  });
  return installPromise;
}

async function doEnsureRatInstalled(
  opts: { manual?: boolean; force?: boolean },
): Promise<string> {
  if (!context) {
    throw new Error(manualInstallMessage());
  }

  const managed = managedRatPath();
  if (!opts.force && managed && await canRunRat(managed)) {
    return exposeRatOnUserPath(managed);
  }

  const mode = vscode.workspace
    .getConfiguration("rat")
    .get<string>("autoInstall", "prompt");

  if (!opts.manual && mode === "never") {
    throw new Error(manualInstallMessage());
  }

  const picked = await vscode.window.showInformationMessage(
    opts.force
      ? "Install or update the Rat CLI into VS Code storage and your normal terminal PATH?"
      : "Rat CLI is required to run cells, but it was not found. Install it into VS Code storage and your normal terminal PATH now?",
    { modal: opts.manual ?? false },
    opts.force ? "Install/Update Rat" : "Install Rat",
    "Set rat.path",
    "Open Docs",
  );

  if (picked === "Set rat.path") {
    const selected = await pickRatExecutable();
    if (!selected) throw new Error("Rat CLI path was not set.");
    return selected;
  }

  if (picked === "Open Docs") {
    await vscode.env.openExternal(vscode.Uri.parse("https://runanything.dev"));
    throw new Error(manualInstallMessage());
  }

  if (picked !== "Install Rat" && picked !== "Install/Update Rat") {
    throw new Error(manualInstallMessage());
  }

  return vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: opts.force ? "Rat: installing/updating CLI…" : "Rat: installing CLI…",
      cancellable: false,
    },
    async (progress) => {
      progress.report({ message: "Detecting platform" });
      const assets = releaseAssetNames();
      if (assets.length === 0) {
        throw new Error(
          `Automatic Rat CLI install is not available for ${process.platform}/${process.arch}. ${manualInstallMessage()}`,
        );
      }

      const dest = managedRatPath();
      if (!dest) throw new Error("Rat installer is not initialized.");
      const tmp = `${dest}.download-${Date.now()}`;

      await fs.promises.mkdir(path.dirname(dest), { recursive: true });

      try {
        progress.report({ message: "Downloading rat" });
        const asset = await downloadFirstAvailable(assets, tmp);

        progress.report({ message: "Checking download integrity" });
        await verifyDownloadChecksum(asset, tmp);

        if (process.platform !== "win32") {
          await fs.promises.chmod(tmp, 0o755);
        }

        progress.report({ message: "Installing binary" });
        await fs.promises.rename(tmp, dest);

        progress.report({ message: "Verifying install" });
        await verifyRat(dest);

        progress.report({ message: "Adding rat to terminal PATH" });
        return exposeRatOnUserPath(dest);
      } catch (err) {
        await fs.promises.rm(tmp, { force: true }).catch(() => undefined);
        throw err;
      }
    },
  );
}

async function exposeRatOnUserPath(source: string): Promise<string> {
  try {
    const installed = await installRatToUserPath(source);
    if (!installed.onPath) {
      vscode.window.showWarningMessage(
        `Rat CLI installed at ${installed.path}, but ${path.dirname(installed.path)} is not currently on PATH. Add it to PATH so terminal sessions can run \`rat\`.`,
      );
    }
    return installed.path;
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showWarningMessage(
      `Rat CLI is installed for this extension, but could not be added to your terminal PATH: ${msg}`,
    );
    return source;
  }
}

async function installRatToUserPath(
  source: string,
): Promise<{ path: string; onPath: boolean }> {
  const target = userPathRatPath();
  if (!target) throw new Error("Could not choose a user PATH install location.");

  if (samePath(source, target)) {
    await verifyRat(target);
    return { path: target, onPath: dirIsOnPath(path.dirname(target)) };
  }

  await fs.promises.mkdir(path.dirname(target), { recursive: true });
  const tmp = `${target}.download-${Date.now()}`;
  try {
    await fs.promises.copyFile(source, tmp);
    if (process.platform !== "win32") {
      await fs.promises.chmod(tmp, 0o755);
    }
    await fs.promises.rename(tmp, target);
    await verifyRat(target);
    return { path: target, onPath: dirIsOnPath(path.dirname(target)) };
  } catch (err) {
    await fs.promises.rm(tmp, { force: true }).catch(() => undefined);
    throw err;
  }
}

async function pickRatExecutable(): Promise<string | undefined> {
  const picked = await vscode.window.showOpenDialog({
    title: "Select rat executable",
    canSelectFiles: true,
    canSelectFolders: false,
    canSelectMany: false,
    openLabel: "Use this rat",
  });

  const file = picked?.[0]?.fsPath;
  if (!file) return undefined;

  await vscode.workspace
    .getConfiguration("rat")
    .update("path", file, vscode.ConfigurationTarget.Global);
  return file;
}

function ratExecutableName(): string {
  return process.platform === "win32" ? "rat.exe" : "rat";
}

function userPathInstallDir(): string | null {
  const explicit = process.env.RAT_VSCODE_INSTALL_DIR;
  if (explicit?.trim()) return expandHome(explicit.trim());

  const existing = findRatOnPathSync();
  if (existing && isUnderHome(existing)) {
    return path.dirname(existing);
  }

  for (const dir of pathDirs()) {
    if (isUnderHome(dir) && fs.existsSync(dir)) return dir;
  }

  const home = osHomeDir();
  if (!home) return null;

  if (process.platform === "win32") {
    const local = process.env.LOCALAPPDATA;
    return local ? path.join(local, "Programs", "rat", "bin") : path.join(home, "bin");
  }

  return path.join(home, ".local", "bin");
}

function findRatOnPathSync(): string | null {
  const names = process.platform === "win32"
    ? ["rat.exe", "rat.cmd", "rat.bat", "rat"]
    : ["rat"];

  for (const dir of pathDirs()) {
    for (const name of names) {
      const candidate = path.join(dir, name);
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return null;
}

function pathDirs(): string[] {
  return (process.env.PATH ?? "")
    .split(path.delimiter)
    .filter((entry) => entry.trim().length > 0)
    .map((entry) => path.resolve(expandHome(entry)));
}

function dirIsOnPath(dir: string): boolean {
  const resolved = path.resolve(dir);
  return pathDirs().some((entry) => samePath(entry, resolved));
}

function isUnderHome(value: string): boolean {
  const home = osHomeDir();
  if (!home) return false;
  const rel = path.relative(path.resolve(home), path.resolve(value));
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

function expandHome(value: string): string {
  const home = osHomeDir();
  if (!home) return value;
  return value === "~" || value.startsWith(`~${path.sep}`)
    ? path.join(home, value.slice(2))
    : value;
}

function osHomeDir(): string {
  return process.env.HOME || process.env.USERPROFILE || "";
}

function samePath(a: string, b: string): boolean {
  const ra = path.resolve(a);
  const rb = path.resolve(b);
  return process.platform === "win32"
    ? ra.toLowerCase() === rb.toLowerCase()
    : ra === rb;
}

function releaseAssetNames(): string[] {
  let osName: string;
  switch (process.platform) {
    case "linux":
      osName = "linux";
      break;
    case "darwin":
      osName = "darwin";
      break;
    case "win32":
      osName = "windows";
      break;
    default:
      return [];
  }

  let archName: string;
  switch (process.arch) {
    case "x64":
      archName = "amd64";
      break;
    case "arm64":
      archName = "arm64";
      break;
    default:
      return [];
  }

  const base = `rat-${osName}-${archName}`;
  return process.platform === "win32" ? [`${base}.exe`, base] : [base];
}

async function downloadFirstAvailable(assets: string[], dest: string): Promise<string> {
  const errors: string[] = [];
  for (const asset of assets) {
    const url = `${DOWNLOAD_BASE_URL}/${asset}`;
    try {
      await fs.promises.rm(dest, { force: true }).catch(() => undefined);
      await downloadFile(url, dest);
      return asset;
    } catch (err) {
      errors.push(`${asset}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
  throw new Error(`Could not download rat for ${process.platform}/${process.arch}. Tried: ${errors.join("; ")}`);
}

async function verifyDownloadChecksum(asset: string, file: string): Promise<void> {
  const expected = await expectedSha256(asset);
  if (!expected) return;

  const actual = await sha256File(file);
  if (actual.toLowerCase() !== expected.toLowerCase()) {
    throw new Error(
      `Downloaded rat checksum mismatch for ${asset}: expected ${expected}, got ${actual}`,
    );
  }
}

async function expectedSha256(asset: string): Promise<string | null> {
  const configured = process.env.RAT_VSCODE_SHA256;
  if (configured) return parseSha256ChecksumText(configured, asset);

  const perAsset = await downloadTextOptional(`${CHECKSUM_BASE_URL}/${asset}.sha256`);
  const fromPerAsset = parseSha256ChecksumText(perAsset, asset);
  if (fromPerAsset) return fromPerAsset;

  for (const name of ["SHA256SUMS", "checksums.txt", "sha256sums.txt"]) {
    const text = await downloadTextOptional(`${CHECKSUM_BASE_URL}/${name}`);
    const parsed = parseSha256ChecksumText(text, asset);
    if (parsed) return parsed;
  }

  return null;
}

export function parseSha256ChecksumText(
  text: string | null | undefined,
  asset: string,
): string | null {
  if (!text) return null;
  const direct = text.trim().match(/^[a-fA-F0-9]{64}$/);
  if (direct) return direct[0];

  const escapedAsset = escapeRegExp(asset);
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;

    const assetLine = trimmed.match(new RegExp(`^([a-fA-F0-9]{64})\\s+\\*?(.*/)?${escapedAsset}$`));
    if (assetLine) return assetLine[1];

    const bsdLine = trimmed.match(new RegExp(`^SHA256 \\((.*/)?${escapedAsset}\\) = ([a-fA-F0-9]{64})$`, "i"));
    if (bsdLine) return bsdLine[2];

    const hashOnly = trimmed.match(/^([a-fA-F0-9]{64})\b/);
    if (hashOnly && !trimmed.includes(" ")) return hashOnly[1];
  }

  return null;
}

function downloadFile(url: string, dest: string, redirects = 0): Promise<void> {
  return new Promise((resolve, reject) => {
    if (redirects > 5) {
      reject(new Error("Too many redirects while downloading rat."));
      return;
    }

    const req = https.get(
      url,
      { headers: { "User-Agent": "rat-vscode" } },
      (res) => {
        const status = res.statusCode ?? 0;
        const location = res.headers.location;

        if ([301, 302, 303, 307, 308].includes(status) && location) {
          res.resume();
          const next = new URL(location, url).toString();
          downloadFile(next, dest, redirects + 1).then(resolve, reject);
          return;
        }

        if (status !== 200) {
          let body = "";
          res.on("data", (chunk: Buffer) => {
            body += chunk.toString();
          });
          res.on("end", () => {
            reject(
              new Error(
                `Download failed (${status}) from ${url}${body ? `: ${body.slice(0, 200)}` : ""}`,
              ),
            );
          });
          return;
        }

        const file = fs.createWriteStream(dest, { mode: 0o755 });
        file.on("error", reject);
        file.on("finish", () => {
          file.close((err) => (err ? reject(err) : resolve()));
        });
        res.pipe(file);
      },
    );

    req.setTimeout(INSTALL_TIMEOUT_MS, () => {
      req.destroy(new Error("Timed out while downloading rat."));
    });
    req.on("error", reject);
  });
}

function downloadTextOptional(url: string, redirects = 0): Promise<string | null> {
  return new Promise((resolve, reject) => {
    if (redirects > 5) {
      resolve(null);
      return;
    }

    const req = https.get(
      url,
      { headers: { "User-Agent": "rat-vscode" } },
      (res) => {
        const status = res.statusCode ?? 0;
        const location = res.headers.location;

        if ([301, 302, 303, 307, 308].includes(status) && location) {
          res.resume();
          const next = new URL(location, url).toString();
          downloadTextOptional(next, redirects + 1).then(resolve, reject);
          return;
        }

        if (status === 404) {
          res.resume();
          resolve(null);
          return;
        }

        if (status !== 200) {
          res.resume();
          resolve(null);
          return;
        }

        let data = "";
        res.setEncoding("utf8");
        res.on("data", (chunk: string) => {
          data += chunk;
        });
        res.on("end", () => resolve(data));
      },
    );

    req.setTimeout(VERIFY_TIMEOUT_MS, () => {
      req.destroy(new Error("Timed out while downloading rat checksum."));
    });
    req.on("error", () => resolve(null));
  });
}

function sha256File(file: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha256");
    const stream = fs.createReadStream(file);
    stream.on("error", reject);
    stream.on("data", (chunk) => hash.update(chunk));
    stream.on("end", () => resolve(hash.digest("hex")));
  });
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function verifyRat(executable: string): Promise<void> {
  return new Promise((resolve, reject) => {
    cp.execFile(
      executable,
      ["version"],
      { timeout: VERIFY_TIMEOUT_MS, env: { ...process.env } },
      (err) => {
        if (err) {
          reject(new Error(`Installed rat but could not run it: ${err.message}`));
          return;
        }
        resolve();
      },
    );
  });
}

function canRunRat(executable: string): Promise<boolean> {
  return new Promise((resolve) => {
    cp.execFile(
      executable,
      ["version"],
      { timeout: VERIFY_TIMEOUT_MS, env: { ...process.env } },
      (err) => resolve(!err),
    );
  });
}

function shellQuote(value: string): string {
  if (process.platform === "win32") {
    return /[\s&()^!"%]/.test(value) ? `"${value.replace(/"/g, '\\"')}"` : value;
  }
  return /^[A-Za-z0-9_/:=@%+.,-]+$/.test(value)
    ? value
    : `'${value.replace(/'/g, `'"'"'`)}'`;
}

function manualInstallMessage(): string {
  if (process.platform === "win32") {
    return "Install rat manually with: irm https://runanything.dev/install.ps1 | iex";
  }
  return "Install rat manually with: curl -fsSL https://runanything.dev/install.sh | sh";
}
