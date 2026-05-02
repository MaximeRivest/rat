/**
 * installRat.ts — first-use installer for the rat CLI.
 *
 * VS Code extensions do not get a reliable "post-install" hook where they can
 * run arbitrary setup. Instead, we make the CLI self-serve on first use:
 * when a rat command is missing, ask the user, download the platform binary
 * into the extension's global storage, and have the extension use that path.
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
  const exe = process.platform === "win32" ? "rat.exe" : "rat";
  return path.join(context.globalStorageUri.fsPath, "bin", exe);
}

/**
 * Resolve the binary the extension should execute.
 *
 * Priority:
 * 1. User setting rat.path when explicitly set to something other than "rat".
 * 2. Extension-managed binary, if already installed.
 * 3. Plain "rat" on PATH.
 */
export function resolveRatExecutable(): string {
  const configured = vscode.workspace
    .getConfiguration("rat")
    .get<string>("path", "rat")
    .trim();

  if (configured && configured !== "rat") return configured;

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
  const existing = resolveRatExecutable();
  if (await canRunRat(existing)) {
    vscode.window.showInformationMessage(`Rat CLI is ready: ${existing}`);
    return;
  }

  const installed = await ensureRatInstalled({ manual: true });
  vscode.window.showInformationMessage(`Rat CLI installed: ${installed}`);
}

/**
 * Ensure a working rat CLI is available. Used after execFile reports ENOENT.
 * The user is always asked before anything is downloaded.
 */
export async function ensureRatInstalled(
  opts: { manual?: boolean } = {},
): Promise<string> {
  if (installPromise) return installPromise;

  installPromise = doEnsureRatInstalled(opts).finally(() => {
    installPromise = null;
  });
  return installPromise;
}

async function doEnsureRatInstalled(
  opts: { manual?: boolean },
): Promise<string> {
  if (!context) {
    throw new Error(manualInstallMessage());
  }

  const managed = managedRatPath();
  if (managed && await canRunRat(managed)) {
    return managed;
  }

  const mode = vscode.workspace
    .getConfiguration("rat")
    .get<string>("autoInstall", "prompt");

  if (!opts.manual && mode === "never") {
    throw new Error(manualInstallMessage());
  }

  const picked = await vscode.window.showInformationMessage(
    "Rat CLI is required to run cells, but it was not found. Install it into VS Code's extension storage now?",
    { modal: opts.manual ?? false },
    "Install Rat",
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

  if (picked !== "Install Rat") {
    throw new Error(manualInstallMessage());
  }

  return vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: "Rat: installing CLI…",
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
        return dest;
      } catch (err) {
        await fs.promises.rm(tmp, { force: true }).catch(() => undefined);
        throw err;
      }
    },
  );
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
