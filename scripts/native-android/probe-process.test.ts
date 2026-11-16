import { expect, test } from "bun:test";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { startProbeProcess } from "./probe-process";

test("each probe observes only its own launch output", async () => {
  const folder = await mkdtemp(join(tmpdir(), "native-probe-process-"));
  try {
    const log = join(folder, "probe.log");
    await writeFile(log, "INSTRUMENTATION_STATUS: probe=ready\nold crash marker and old trailing text\n");
    const current = await startProbeProcess(["/usr/bin/printf", "fresh\n"], log);
    expect(await current.exited).toBe(0);
    expect(await readFile(log, "utf8")).toBe("fresh\n");
  } finally {
    await rm(folder, { recursive: true });
  }
});

test("a finishing prior probe cannot append stale output after the log is cleared", async () => {
  const folder = await mkdtemp(join(tmpdir(), "native-probe-process-"));
  try {
    const log = join(folder, "probe.log");
    const prior = Bun.spawn([process.execPath, "-e",
      'await Bun.sleep(100); process.stdout.write("old late crash marker\\n");'],
      { stdout: Bun.file(log), stderr: "ignore" });
    const current = await startProbeProcess(["/usr/bin/printf", "fresh\n"], log, prior.exited);
    expect(prior.exitCode).toBe(0);
    expect(await current.exited).toBe(0);
    expect(await readFile(log, "utf8")).toBe("fresh\n");
  } finally {
    await rm(folder, { recursive: true });
  }
});
