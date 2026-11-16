import { writeFile } from "node:fs/promises";

/** Serialize owned probe launches and discard the prior launch's log tail. */
export async function startProbeProcess(command: string[], log: string, previousExit?: Promise<number>) {
  if (previousExit) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([previousExit, new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(Error("Previous disposable instrumentation did not exit")), 10_000);
      })]);
    } finally {
      clearTimeout(timer);
    }
  }
  // Bun's subprocess file output overwrites from offset zero without truncating.
  // The previous client must have exited before this file can safely be reset.
  await writeFile(log, "", { mode: 0o600 });
  return Bun.spawn(command, { stdout: Bun.file(log), stderr: "ignore" });
}
