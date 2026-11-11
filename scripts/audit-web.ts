import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function assessAudit(
  report: unknown,
  exitCode: number | null,
  _lock?: unknown,
): { exceptions: number } {
  if (![0, 1].includes(exitCode as number) || !object(report)) {
    throw new Error("Bun audit did not return a valid advisory report");
  }
  for (const [name, entries] of Object.entries(report)) {
    if (!Array.isArray(entries) || entries.length === 0) {
      throw new Error("Bun audit returned malformed advisory entries");
    }
    throw new Error(
      `Unresolved audit finding for ${name}; see the raw report above`,
    );
  }
  if (exitCode !== 0)
    throw new Error("Bun audit failed without a usable advisory report");
  return { exceptions: 0 };
}

if (import.meta.main) {
  try {
    const result = spawnSync(process.execPath, ["audit", "--json"], {
      cwd: fileURLToPath(new URL("../web/", import.meta.url)),
      encoding: "utf8",
      timeout: 120_000,
      maxBuffer: 16 * 1024 * 1024,
    });
    process.stdout.write(result.stdout || "");
    process.stderr.write(result.stderr || "");
    if (result.error || result.signal)
      throw result.error || new Error("Bun audit was interrupted");
    assessAudit(JSON.parse(result.stdout), result.status);
    console.log("Web audit passed: 0 exceptions, 0 unresolved findings.");
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
