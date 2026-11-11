// Keep this error boundary separate from fixture data: Playwright includes the
// surrounding source in failure reports even when page snapshots are disabled.
export async function privateAction<T>(work: () => Promise<T>): Promise<T> {
  try {
    return await work();
  } catch {
    throw new Error("The private fixture action failed.");
  }
}
