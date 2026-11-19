export type NativeIdentityLane = "preview" | "app";

const identities = {
  preview: {
    lane: "preview",
    appId: "com.gkldevelopment.mychef.preview",
    appName: "MyChef Preview",
  },
  app: { lane: "app", appId: "dev.mychef.app", appName: "MyChef" },
} as const;

// Runtime callers include generated build inputs, not just typed TS callers.
export function nativeIdentity(lane: unknown) {
  if (lane !== "preview" && lane !== "app")
    throw new Error("Select the explicit preview or app identity.");
  return identities[lane];
}

// Inspect compiler-produced metadata. Prepared configuration alone is not a
// packaged identity receipt; unsigned compilation is not signing proof.
export function verifyCompiledIdentity(platform:"android"|"ios-simulator"|"ios-device",metadata:string,lane:NativeIdentityLane,version:{version:string;build:number}):string {
 const expected=nativeIdentity(lane).appId;
 if(platform==="android"){
  const rows=metadata.split("\n").filter(line=>line.startsWith("package: "));
  const row=rows[0]??"";
  if(rows.length!==1 || row.match(/name='([^']+)'/)?.[1]!==expected || row.match(/versionCode='([^']+)'/)?.[1]!==String(version.build) || row.match(/versionName='([^']+)'/)?.[1]!==version.version || !metadata.split("\n").some(line=>line.startsWith("launchable-activity: name='com.gkldevelopment.mychef.preview.MainActivity' ")))throw new Error("Compiled native artifact identity does not match its selected lane.");
 }else{
  const info=JSON.parse(metadata);
  if(info.CFBundleIdentifier!==expected || info.CFBundleVersion!==String(version.build) || info.CFBundleShortVersionString!==version.version)throw new Error("Compiled native artifact identity does not match its selected lane.");
 }
 return expected;
}
