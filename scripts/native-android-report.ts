import contract from "../mobile/tests/analytics-contract.json";

export type AnalyticsBinding = {
  sourceCommit: string;
  apiSha256: string;
  bundleSha256: string;
  artifactSha256: string;
  fixtureConfigSha256: string;
  contractVersion: number;
};
const bindingKeys = [
  "sourceCommit",
  "apiSha256",
  "bundleSha256",
  "artifactSha256",
  "fixtureConfigSha256",
  "contractVersion",
];
function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function keys(value: Record<string, unknown>, names: string[]) {
  return (
    Object.keys(value).length === names.length &&
    names.every((name) => Object.hasOwn(value, name))
  );
}
/** Validate bounded observations against identities measured by the runner.
 * This validates structure and bindings; it does not authenticate JSON or prove
 * a device run. Only the guarded runner may issue the retained receipt.
 */
export function validateAnalyticsProbe(
  value: unknown,
  platform: string,
  expected: AnalyticsBinding,
): boolean {
  if (
    !record(value) ||
    !keys(value, ["schemaVersion", "platform", "binding", "checks"]) ||
    value.schemaVersion !== contract.schemaVersion ||
    value.platform !== platform ||
    !["android", "ios-simulator"].includes(platform) ||
    !record(value.binding) ||
    !keys(value.binding, bindingKeys) ||
    !record(expected) ||
    !keys(expected, bindingKeys)
  )
    return false;
  for (const key of bindingKeys) {
    const actual = value.binding[key];
    if (actual !== expected[key as keyof AnalyticsBinding]) return false;
    if (key === "contractVersion") {
      if (actual !== contract.contractVersion) return false;
    } else if (
      typeof actual !== "string" ||
      !(key === "sourceCommit" ? /^[a-f0-9]{40}$/ : /^[a-f0-9]{64}$/).test(
        actual,
      )
    )
      return false;
  }
  if (
    !record(value.checks) ||
    !keys(value.checks, Object.keys(contract.checks))
  )
    return false;
  for (const [group, names] of Object.entries(contract.checks)) {
    const observed = value.checks[group];
    if (
      !record(observed) ||
      !keys(observed, names) ||
      !names.every((name) => observed[name] === true)
    )
      return false;
  }
  return true;
}

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

/** Raw emulator crash logs stay private; only closed categories reach CI. */
export function androidExitFacts(raw: string, expectedPid?: string) {
  const empty = { currentProcess: false, reason: "unknown", status: "unknown" };
  if (raw.length > 131072 || !expectedPid || !/^[1-9][0-9]{0,9}$/.test(expectedPid)) return empty;
  for (const block of raw.split(/(?=ApplicationExitInfo #[0-9]+:)/)) {
    if (block.match(/\bpid=([0-9]+)\b/)?.[1] !== expectedPid
        || !/\bprocess=com\.gkldevelopment\.mychef\.preview(?:\s|$)/.test(block)) continue;
    return { currentProcess: true,
      reason: block.match(/\breason=([0-9]{1,2})\b/)?.[1] ?? "unknown",
      status: block.match(/\bstatus=([0-9]{1,3})\b/)?.[1] ?? "unknown" };
  }
  return empty;
}

export function androidCrashFacts(raw: string, expectedPid?: string) {
  const empty = { targetProcess: false, javaCrash: false, nativeCrash: false,
    rendererFailure: false, exceptions: [] as string[], frames: [] as string[],
    signals: [] as string[], libraries: [] as string[], abortClasses: [] as string[],
    threadRole: "unknown", nativeFrameCategories: [] as string[] };
  if (raw.length > 131072) return empty;
  if (expectedPid !== undefined && !/^[1-9][0-9]{0,9}$/.test(expectedPid)) return empty;
  const lines = raw.split(/\r?\n/);
  const target = /Process: com\.gkldevelopment\.mychef\.preview(?:,|$)|>>> com\.gkldevelopment\.mychef\.preview <<</;
  let index = -1;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    if (target.test(line) && (expectedPid === undefined
        || line.match(/\b(?:PID|pid):\s*([0-9]+)\b/)?.[1] === expectedPid)) index = i;
  }
  if (index < 0) return empty;
  let end = index + 1;
  while (end < lines.length && !/FATAL EXCEPTION:|\bProcess: |>>> .* <<</.test(lines[end]!)) end++;
  const start = index > 0 && /FATAL EXCEPTION:/.test(lines[index - 1]!) ? index - 1 : index;
  const block = lines.slice(start, end).join("\n");
  const known = ["SecurityException", "NullPointerException", "IllegalStateException",
    "FileUriExposedException", "RuntimeException", "UnsupportedOperationException",
    "OutOfMemoryError", "NoSuchMethodError", "UnsatisfiedLinkError"];
  const frames = [...block.matchAll(/\bat\s+(?:com\.gkldevelopment\.mychef\.(?:preview|testing)|com\.getcapacitor)\.[\w.$]+\(((?:ProtectedRefresh|ProtectedShopping|MyChefNativePlugin|MainActivity|NativeBoundary|NativeRuntime|NativeMediaFiles|NativeJourneyProbe|Bridge|BridgeWebViewClient|WebViewLocalServer|PluginCall|PluginHandle)\.java):([0-9]{1,5})\)/g)]
    .map(match => match[1] + ":" + match[2]);
  const thread = block.match(/\bname:\s*([^>\n]{1,64})\s+>>>/)?.[1]?.trim() ?? "";
  const header = lines[index];
  if (header === undefined) return empty;
  const identity = header.match(/\bpid:\s*([1-9][0-9]{0,9}),\s*tid:\s*([1-9][0-9]{0,9}),/);
  const mainThread = identity !== null && identity[1] === identity[2];
  const threadRole = mainThread ? "main" : ([ ["main", /^main$/], ["render", /^RenderThread$/],
    ["binder", /^Binder:/], ["instrumentation", /^Instr:/], ["automation", /^UiAutomation/],
    ["gc", /^(HeapTaskDaemon|FinalizerDaemon)$/], ["jit", /^Jit thread pool$/] ] as const)
    .find(([, pattern]) => pattern.test(thread))?.[0] ?? "unknown";
  // Restrict symbol recognition to native stack lines. Keep names, addresses,
  // paths, thread ids and arbitrary crash messages out of the result.
  const nativeFrames = block.split("\n").filter(line =>
    /(?:^\s*|\bDEBUG\s*:\s*)#[0-9]{2,3}\s+pc\s+[0-9a-f]{1,16}\s+/.test(line)).join("\n");
  const nativeFrameCategories = ([
    ["jni-reference", /DecodeJObject|NewGlobalRef|DeleteGlobalRef|JNIEnvExt|ScopedCheck|CheckJNI/],
    ["art-interpreter", /art::interpreter|ExecuteNterpImpl|nterp_helper|art_quick_to_interpreter/],
    ["art-gc", /art::gc::|Heap::CollectGarbage|MarkCompact::/],
    ["art-jit", /art::jit::|JitCompileTask|JitCompiler/],
    ["ui-automation", /UiAutomationConnection|UiAutomation::/],
    ["input-event", /android_view_(?:InputEvent|MotionEvent)|NativeInputEventReceiver|finishInputEvent/],
    ["accessibility", /android_view_accessibility|AccessibilityInteractionClient|AccessibilityInteractionController|AccessibilityNodeInfo|AccessibilityRecord/],
    ["binder", /android_os_Binder|IPCThreadState|BBinder|BpBinder/],
    ["jni-call", /Call(?:Static)?(?:Void|Object|Boolean|Int)Method/],
    ["test-probe", /com[/.]gkldevelopment[/.]mychef[/.]testing[/.]NativeJourneyProbe[.$/]/],
    ["app-bridge", /com[/.]gkldevelopment[/.]mychef[/.]preview[/.](?:MyChefNativePlugin|ProtectedRefresh|ProtectedShopping|NativeRuntime)[.$/]/],
    ["jni-weak-reference", /NewWeakGlobalRef|DeleteWeakGlobalRef|DecodeWeakGlobal/],
    ["display-event", /NativeDisplayEventReceiver|NativeChoreographer/],
    ["runtime-loop", /NativeMessageQueue|android_os_MessageQueue|Looper::/],
    ["binder-object", /javaObjectForIBinder|ibinderForJavaObject|JavaDeathRecipient/],
    ["webview-native", /android_webview::|AwContents::|AwBrowserContext::/],
  ] as const).filter(([, pattern]) => pattern.test(nativeFrames)).map(([kind]) => kind);
  return { targetProcess: true, javaCrash: block.includes("FATAL EXCEPTION:"),
    nativeCrash: /signal [0-9]{1,2} \(SIG(?:ABRT|SEGV|BUS|ILL)\)/.test(block),
    rendererFailure: /[Rr]ender(?:er| process).*crash|lib(?:webviewchromium|monochrome)\.so/.test(block),
    exceptions: known.filter(name => new RegExp("\\b" + name + "\\b").test(block)),
    frames: [...new Set(frames)].slice(0, 8),
    signals: ["SIGABRT", "SIGSEGV", "SIGBUS", "SIGILL"].filter(name => block.includes("(" + name + ")")),
    libraries: ["libc.so", "libart.so", "libandroid_runtime.so", "libhwui.so", "libEGL.so", "libGLESv2.so",
      "libwebviewchromium.so", "libmonochrome.so"].filter(name => block.includes("/" + name + " ") || block.includes("/" + name + "\n") || block.endsWith("/" + name)),
    threadRole, nativeFrameCategories,
    abortClasses: ([ ["jni", /JNI DETECTED ERROR/], ["allocation", /out of memory|Failed to allocate|allocation failed/i],
      ["thread", /Failed to create thread|pthread_create.*failed/i], ["scudo", /Scudo ERROR/],
      ["assertion", /Check failed:|assertion.*failed/i] ] as const).filter(([, pattern]) => pattern.test(block)).map(([kind]) => kind) };
}
