/**
 * installRat.ts — first-use installer for the rat CLI.
 *
 * VS Code extensions do not get a reliable "post-install" hook where they can
 * run arbitrary setup. Instead, we make the CLI self-serve on first use:
 * when a rat command is missing, ask the user, download the platform binary
 * into the extension's global storage, and have the extension use that path.
 */

import * as cp from "child_process";
import * as fs from "fs";
import * as https from "https";
import * as path from "path";
import * as vscode from "vscode";

let context: vscode.ExtensionContext | null = null;
let installPromise: Promise<string> | null = null;

const DOWNLOAD_BASE_URL =
  process.env.RAT_VSCODE_DOWNLOAD_BASE_URL ??
  "https://github.com/maximerivest/rat/releases/latest/download";

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
        await downloadFirstAvailable(assets, tmp);

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

async function downloadFirstAvailable(assets: string[], dest: string): Promise<void> {
  const errors: string[] = [];
  for (const asset of assets) {
    const url = `${DOWNLOAD_BASE_URL}/${asset}`;
    try {
      await fs.promises.rm(dest, { force: true }).catch(() => undefined);
      await downloadFile(url, dest);
      return;
    } catch (err) {
      errors.push(`${asset}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
  throw new Error(`Could not download rat for ${process.platform}/${process.arch}. Tried: ${errors.join("; ")}`);
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
