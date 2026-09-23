// Types for rat-notebook.js (mrmd-editor src/rat-notebook.js), vendored.
export const GENERATED_ASSETS_DIR: string;
export function fenceLanguage(line: string): string;
export function isOutputFence(line: string): boolean;
export function isOwnedImageLine(line: string): boolean;
export function cleanRunOutput(out: string): string;
export function splitPlots(text: string): { text: string; plots: string[] };
export function createLiveOutputFilter(): {
  feed(chunk: string): { text: string; plots: string[] };
  flush(): { text: string; plots: string[] };
};
export function fenceFor(text: string): string;
export function formatResult(text: string, images?: { src: string; alt?: string }[]): string;
export function finishedOutput(out: string): { text: string; plots: string[] };
export function cellForCode<T extends { code: string }>(cells: T[], code: string): T | null;
export function createRunFollower(): {
  runs: Map<string, unknown>;
  apply(ev: Record<string, unknown>): { kind: string; run: unknown; text?: string; plots?: string[] };
};
