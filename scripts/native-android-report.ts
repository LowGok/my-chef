/** Keep fixed phase and protocol errors, never credentials, addresses or bodies. */
export function safeJourneyError(
  error: unknown,
  stage: string,
  secrets: string[] = [],
) {
  const phase = /^[a-z0-9 -]{1,100}$/i.test(stage) ? stage : "unknown";
  const kind =
    error instanceof Error && /^[A-Za-z]{1,40}$/.test(error.name)
      ? error.name
      : "Failure";
  let reason = error instanceof Error ? error.message.split(/\r?\n/)[0] : "";
  for (const secret of secrets)
    if (secret) reason = reason.split(secret).join("[private]");
  reason = reason
    .replace(/Bearer\s+\S+/gi, "[credential]")
    .replace(/https?:\/\/\S+/gi, "[url]")
    .replace(/[^\s<>"']+@[^\s<>"']+/g, "[address]")
    .replace(/[A-Za-z0-9_-]{43,}/g, "[opaque]")
    .slice(0, 350);
  const location =
    error instanceof Error
      ? error.stack?.match(/native-android-journeys\.ts:\d+:\d+/)?.[0]
      : undefined;
  return (
    "Installed Android journey failed at stage: " +
    phase +
    "; " +
    kind +
    (reason ? "; " + reason : "") +
    (location ? "; " + location : "")
  );
}
