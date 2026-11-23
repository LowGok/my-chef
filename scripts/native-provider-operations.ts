import {createHash,randomUUID} from "node:crypto";
import {lstat,mkdir,open,readFile,rename,unlink} from "node:fs/promises";
import {dirname,join} from "node:path";

type Provider="apple"|"google"|"website";
type Target=Record<string,string|number|boolean|string[]>;
type Artifact={kind:"archive"|"ipa"|"aab"|"association-bundle";appId:string;sourceCommit:string;sha256:string;bundleSha256?:string};
export type NativeProviderPlan={schemaVersion:1;operationId:string;identityLane:"app";appId:string;environment:"preview";provider:Provider;accountId:string;sourceCommit:string;action:string;target:Target;beforeSha256?:string;artifact?:Artifact};
export type NativeResourceReceipt={provider:Provider;accountId:string;appId:string;environment:string;action:string;resourceId:string;target:Target;artifactSha256?:string};
// Adapters obtain context/readback from authenticated provider access and
// artifact facts from the actual immutable files. Caller JSON is not proof.
export type NativeProviderClient={
 context():Promise<{provider:string;accountId:string;environment:string;allowedActions:string[]}>;
 find(plan:NativeProviderPlan):Promise<NativeResourceReceipt[]>;
 before(plan:NativeProviderPlan):Promise<{provider:string;accountId:string;environment:string;resourceId:string;sha256:string;appId?:string;origin?:string;billingEnabled?:boolean;analyticsEnabled?:boolean}>;
 artifact?(plan:NativeProviderPlan):Promise<Artifact>;
 apply(plan:NativeProviderPlan):Promise<void|"rejected">;
};
const fail=():never=>{throw new Error("Native provider operation scope is not verified.");};
const pending=():never=>{throw new Error("Native provider write needs readback reconciliation before another write.");};
const sha=(v:string)=>createHash("sha256").update(v).digest("hex");
const hex=(v:unknown,n:number)=>typeof v==="string" && new RegExp(`^[a-f0-9]{${n}}$`).test(v);
const id=(v:unknown)=>typeof v==="string" && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/.test(v);
const name=(v:unknown)=>typeof v==="string" && /^[A-Za-z0-9][A-Za-z0-9 ._-]{0,79}$/.test(v);
const cert=(v:unknown)=>typeof v==="string" && /^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$/.test(v);
function obj(v:unknown):Record<string,unknown>{if(!v || typeof v!=="object" || Array.isArray(v))return fail();return v as Record<string,unknown>;}
function exact(v:Record<string,unknown>,required:string[],optional:string[]=[]){const keys=Object.keys(v);if(required.some(k=>!keys.includes(k)) || keys.some(k=>![...required,...optional].includes(k)))fail();}
function canonical(v:unknown):string{
 if(Array.isArray(v))return "["+v.map(canonical).join(",")+"]";
 if(v && typeof v==="object")return "{"+Object.entries(v).filter(([,value])=>value!==undefined).sort(([a],[b])=>a.localeCompare(b)).map(([k,value])=>JSON.stringify(k)+":"+canonical(value)).join(",")+"}";
 const encoded=JSON.stringify(v);if(encoded===undefined)return fail();return encoded;
}
function list(v:unknown,check:(v:unknown)=>boolean,empty=false):v is string[]{return Array.isArray(v) && v.length<65 && (empty || v.length>0) && v.every(check) && new Set(v).size===v.length;}
function privateGroup(t:Record<string,unknown>,empty=false){if(t.internalOnly!==true || t.automaticDistribution!==false || t.publicLink!==false || !list(t.memberIds,id,empty) || empty && (t.memberIds as string[]).length!==0)fail();}

function parse(input:unknown):NativeProviderPlan{
 const p=obj(input);exact(p,["schemaVersion","operationId","identityLane","appId","environment","provider","accountId","sourceCommit","action","target"],["beforeSha256","artifact"]);
 if(p.schemaVersion!==1 || !hex(p.operationId,32) || p.identityLane!=="app" || p.appId!=="dev.mychef.app" || p.environment!=="preview" || !["apple","google","website"].includes(p.provider as string) || !id(p.accountId) || !hex(p.sourceCommit,40))return fail();
 const t=obj(p.target),provider=p.provider;
 let artifactKind:string|undefined;
 switch(p.action){
 case "create-app":
  exact(t,provider==="apple"?["name","sku","platform","userAccess","appUserIds"]:["name","platform"]);
  if(!["apple","google"].includes(provider as string) || t.name!=="MyChef" || t.platform!==(provider==="apple"?"IOS":"ANDROID"))fail();
  if(provider==="apple" && (typeof t.sku!=="string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$/.test(t.sku) || t.userAccess!=="limited" || !list(t.appUserIds,id)))fail();
  break;
 case "register-bundle":
  exact(t,["name","platform"]);if(provider!=="apple" || t.name!=="MyChef" || t.platform!=="IOS")fail();break;
 case "create-group":
  exact(t,["parentId","name","internalOnly","automaticDistribution","publicLink","memberIds"]);if(provider!=="apple" || !id(t.parentId) || !name(t.name))fail();privateGroup(t,true);break;
 case "configure-capability":
  exact(t,["recordId","capability","enabled"]);if(provider!=="apple" || !id(t.recordId) || !["PUSH_NOTIFICATIONS","ASSOCIATED_DOMAINS","APP_ATTEST"].includes(t.capability as string) || t.enabled!==true)fail();break;
 case "create-profile":
  exact(t,["parentId","name","certificateFingerprint","pushEnvironment","associatedDomain","appAttestEnvironment"]);if(provider!=="apple" || !id(t.parentId) || !name(t.name) || !cert(t.certificateFingerprint) || t.pushEnvironment!=="production" || t.appAttestEnvironment!=="production" || t.associatedDomain!=="applinks:mychef.dev")fail();break;
 case "register-firebase":
  exact(t,["parentId","name","platform","billingEnabled","analyticsEnabled"]);if(provider!=="google" || !id(t.parentId) || t.name!=="MyChef" || t.platform!=="ANDROID" || t.billingEnabled!==false || t.analyticsEnabled!==false)fail();break;
 case "sign-archive":
  exact(t,["recordId","parentId","certificateFingerprint","entitlementsSha256","profileExpiresUnix"]);if(provider!=="apple" || !id(t.recordId) || !id(t.parentId) || !cert(t.certificateFingerprint) || !hex(t.entitlementsSha256,64) || !Number.isSafeInteger(t.profileExpiresUnix))fail();artifactKind="archive";break;
 case "initial-play-upload":
  exact(t,["recordId","track","releaseStatus","signingMode","uploadCertificateFingerprint","version","build"]);if(provider!=="google" || !id(t.recordId) || t.track!=="internal" || t.releaseStatus!=="draft" || t.signingMode!=="play-app-signing" || !cert(t.uploadCertificateFingerprint))fail();artifactKind="aab";break;
 case "upload-build":
  exact(t,["recordId","profileId","version","build","internalOnly","signingFingerprints"]);if(provider!=="apple" || !id(t.recordId) || !id(t.profileId) || t.internalOnly!==true || !list(t.signingFingerprints,cert))fail();artifactKind="ipa";break;
 case "set-testers":
  exact(t,["recordId","parentId","internalOnly","automaticDistribution","publicLink","memberIds"]);if(provider!=="apple" || !id(t.recordId) || !id(t.parentId))fail();privateGroup(t);break;
 case "assign-build":
  if(provider==="apple"){
   exact(t,["recordId","parentId","buildId","internalOnly","automaticDistribution","publicLink","memberIds","assignedGroupIds","signingFingerprints"]);
   if(!id(t.recordId) || !id(t.parentId) || !id(t.buildId) || canonical(t.assignedGroupIds)!==canonical([t.recordId]) || !list(t.signingFingerprints,cert))fail();privateGroup(t);artifactKind="ipa";
  }else{
   exact(t,["recordId","track","build","releaseStatus","memberIds","signingFingerprints","licenseTesting"]);
   if(provider!=="google" || !id(t.recordId) || t.track!=="internal" || t.releaseStatus!=="completed" || t.licenseTesting!==true || !list(t.memberIds,id) || !list(t.signingFingerprints,cert))fail();artifactKind="aab";
  }break;
 case "publish-associations":
  exact(t,["recordId","origin","paths","enabled"]);if(provider!=="website" || !id(t.recordId) || t.origin!=="https://mychef.dev" || typeof t.enabled!=="boolean" || canonical(t.paths)!==canonical(["/.well-known/apple-app-site-association","/.well-known/assetlinks.json","/app/open"]))fail();artifactKind="association-bundle";break;
 default:return fail();
 }
 if("version" in t && (typeof t.version!=="string" || !/^\d+\.\d+\.\d+$/.test(t.version)))fail();
 if("build" in t && (!Number.isSafeInteger(t.build) || (t.build as number)<1 || (t.build as number)>2147483647))fail();
 const needsBefore=!["create-app","register-bundle"].includes(p.action as string);
 if(needsBefore && !hex(p.beforeSha256,64) || p.beforeSha256!==undefined && !hex(p.beforeSha256,64))fail();
 if(artifactKind){const a=obj(p.artifact);exact(a,["kind","appId","sourceCommit","sha256"],artifactKind==="association-bundle"?[]:["bundleSha256"]);if(a.kind!==artifactKind || a.appId!==p.appId || a.sourceCommit!==p.sourceCommit || !hex(a.sha256,64) || artifactKind!=="association-bundle" && !hex(a.bundleSha256,64))fail();}
 else if(p.artifact!==undefined)fail();
 // Round-trip only validated, bounded JSON facts. Credentials and raw provider
 // replies are never journal entries or diagnostics.
 return JSON.parse(canonical(p)) as NativeProviderPlan;
}

export function nativeOperationDigest(input:unknown):string{return sha(canonical(parse(input)));}
export function authorizeNativeOperation(input:unknown,approval:unknown,now=Math.floor(Date.now()/1000)):NativeProviderPlan{
 const p=parse(input),a=obj(approval);exact(a,["planSha256","expiresUnix"]);
 if(a.planSha256!==nativeOperationDigest(p) || !Number.isSafeInteger(a.expiresUnix) || (a.expiresUnix as number)<=now || (a.expiresUnix as number)>now+86400 || p.action==="sign-archive" && (p.target.profileExpiresUnix as number)<=now)return fail();
 return p;
}

async function readJSON(path:string):Promise<unknown|null>{
 try{const s=await lstat(path);if(!s.isFile() || s.isSymbolicLink() || s.size>65536 || s.mode&0o077)fail();return JSON.parse(await readFile(path,"utf8"));}
 catch(error){if((error as NodeJS.ErrnoException).code==="ENOENT")return null;throw error;}
}
async function atomicJSON(path:string,value:unknown){
 const temp=path+"."+randomUUID()+".tmp";
 try{
  const file=await open(temp,"wx",0o600);
  try{await file.writeFile(canonical(value));await file.sync();}finally{await file.close();}
  await rename(temp,path);
  const directory=await open(dirname(path),"r");try{await directory.sync();}finally{await directory.close();}
 }
 finally{await unlink(temp).catch(error=>{if(error.code!=="ENOENT")throw error;});}
}
function receipt(plan:NativeProviderPlan,rows:NativeResourceReceipt[]):NativeResourceReceipt|null{
 if(!Array.isArray(rows) || rows.length>1)return fail();if(!rows.length)return null;
 const r=rows[0];if(r.provider!==plan.provider || r.accountId!==plan.accountId || r.appId!==plan.appId || r.environment!==plan.environment || r.action!==plan.action || !id(r.resourceId) || canonical(r.target)!==canonical(plan.target) || r.artifactSha256!==plan.artifact?.sha256 || plan.target.recordId!==undefined && r.resourceId!==plan.target.recordId)return fail();
 return JSON.parse(canonical({provider:r.provider,accountId:r.accountId,appId:r.appId,environment:r.environment,action:r.action,resourceId:r.resourceId,target:r.target,artifactSha256:r.artifactSha256})) as NativeResourceReceipt;
}

export async function runNativeOperation(input:unknown,approval:unknown,client:NativeProviderClient,directory:string,clock:number|(()=>number)=()=>Math.floor(Date.now()/1000)):Promise<NativeResourceReceipt>{
 const now=()=>typeof clock==="function"?clock():clock;
 const plan=authorizeNativeOperation(input,approval,now()),digest=nativeOperationDigest(plan);
 await mkdir(directory,{recursive:true,mode:0o700});const stat=await lstat(directory);if(!stat.isDirectory() || stat.isSymbolicLink() || stat.mode&0o077)return fail();
 const lane=sha(canonical([plan.provider,plan.accountId,plan.identityLane,plan.appId,plan.environment]));
 const lockPath=join(directory,lane+".lock"),pendingPath=join(directory,lane+".pending.json"),recordPath=(key:string)=>join(directory,key+".json");
 const lock=await open(lockPath,"wx",0o600);
 try{
  const checkAccount=async()=>{const c=await client.context();if(c.provider!==plan.provider || c.accountId!==plan.accountId || c.environment!==plan.environment || !Array.isArray(c.allowedActions) || !c.allowedActions.includes(plan.action))fail();};
  await checkAccount();
  const old=await readJSON(pendingPath);
  if(old){const entry=obj(old),previous=parse(entry.plan);if(entry.planSha256!==nativeOperationDigest(previous) || previous.provider!==plan.provider || previous.accountId!==plan.accountId || previous.appId!==plan.appId || previous.environment!==plan.environment)fail();
   const found=receipt(previous,await client.find(previous));if(!found)return pending();
   await atomicJSON(recordPath(nativeOperationDigest(previous)),{state:"verified",planSha256:nativeOperationDigest(previous),resource:found});await unlink(pendingPath);
  }
  const existing=receipt(plan,await client.find(plan));
  if(existing){await atomicJSON(recordPath(digest),{state:"verified",planSha256:digest,resource:existing});return existing;}
  const prior=await readJSON(recordPath(digest));if(prior && obj(prior).state!=="rejected")return pending();
  if(plan.beforeSha256){const before=await client.before(plan);if(before.provider!==plan.provider || before.accountId!==plan.accountId || before.environment!==plan.environment || before.resourceId!==(plan.target.recordId??plan.target.parentId) || before.sha256!==plan.beforeSha256)return fail();
   if(plan.action==="register-firebase"){if(before.billingEnabled!==false || before.analyticsEnabled!==false)fail();}
   else if(plan.action==="publish-associations"){if(before.origin!=="https://mychef.dev")fail();}
   else if(before.appId!==plan.appId)fail();
  }
  if(plan.artifact){if(!client.artifact || canonical(await client.artifact(plan))!==canonical(plan.artifact))return fail();}
  await checkAccount();authorizeNativeOperation(plan,approval,now());
  await atomicJSON(pendingPath,{state:"applying",planSha256:digest,plan});
  let rejected=false;
  try{rejected=(await client.apply(plan))==="rejected";}catch{ /* An unknown response never authorizes a replay. */ }
  await atomicJSON(pendingPath,{state:"uncertain",planSha256:digest,plan});
  const result=receipt(plan,await client.find(plan));
  if(!result){if(rejected){await atomicJSON(recordPath(digest),{state:"rejected",planSha256:digest});await unlink(pendingPath);throw new Error("Native provider rejected the write without applying it.");}return pending();}
  await atomicJSON(recordPath(digest),{state:"verified",planSha256:digest,resource:result});await unlink(pendingPath);return result;
 }finally{await lock.close();await unlink(lockPath);}
}
