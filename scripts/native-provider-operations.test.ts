import {afterEach,expect,test} from "bun:test";
import {mkdtemp,rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import {nativeOperationDigest,runNativeOperation,type NativeProviderClient,type NativeProviderPlan} from "./native-provider-operations";

const temporary:string[]=[];
afterEach(async()=>{for(const p of temporary.splice(0))await rm(p,{recursive:true,force:true});});
const plan=():NativeProviderPlan=>({schemaVersion:1,operationId:"a".repeat(32),identityLane:"app",appId:"dev.mychef.app",environment:"preview",provider:"apple",accountId:"approved-team",sourceCommit:"b".repeat(40),action:"create-app",target:{name:"MyChef",sku:"mychef-app-ios",platform:"IOS",userAccess:"limited",appUserIds:["account-holder"]}});
const approval=(value:NativeProviderPlan)=>({planSha256:nativeOperationDigest(value),expiresUnix:2000});
async function journal(){const directory=await mkdtemp(join(tmpdir(),"mychef-provider-"));temporary.push(directory);return directory;}
function fixture(value:NativeProviderPlan){
 let created=false,writes=0,failAfter=false,failBefore=false;
 const context={provider:value.provider,accountId:value.accountId,environment:"preview",allowedActions:[value.action]};
 const client:NativeProviderClient={async context(){return context;},async find(){return created?[{provider:value.provider,accountId:value.accountId,appId:value.appId,environment:"preview",action:value.action,resourceId:"created-record",target:value.target,artifactSha256:value.artifact?.sha256}]:[];},async before(){throw new Error("root creation has no existing parent");},async apply(){writes++;if(failBefore)throw new Error("uncertain response");created=true;if(failAfter)throw new Error("lost reply");}};
 return {client,context,writes:()=>writes,failAfter:()=>{failAfter=true;},failBefore:()=>{failBefore=true;},makeVisible:()=>{created=true;}};
}

test("creation approval names only values available before creation, then binds returned identity",async()=>{
 const value=plan(),remote=fixture(value),directory=await journal();
 const result=await runNativeOperation(value,approval(value),remote.client,directory,1000);
 expect(result.resourceId).toBe("created-record");expect(remote.writes()).toBe(1);
 expect(await runNativeOperation(value,approval(value),remote.client,directory,1000)).toEqual(result);
 expect(remote.writes()).toBe(1);
});

test("wrong app, stale approval and wrong account are refused before any provider write",async()=>{
 for(const change of ["app","approval","account"]){
  const value=plan(),remote=fixture(value),consent=approval(value);
  if(change==="app")value.appId="com.gkldevelopment.mychef.preview";
  if(change==="approval")value.target.sku="changed-sku";
  if(change==="account")remote.context.accountId="other-team";
  await expect(runNativeOperation(value,consent,remote.client,await journal(),1000)).rejects.toThrow();
  expect(remote.writes()).toBe(0);
 }
});

test("a lost create reply is reconciled by authenticated readback without a second write",async()=>{
 const value=plan(),remote=fixture(value);remote.failAfter();const directory=await journal();
 expect((await runNativeOperation(value,approval(value),remote.client,directory,1000)).resourceId).toBe("created-record");
 await runNativeOperation(value,approval(value),remote.client,directory,1000);
 expect(remote.writes()).toBe(1);
});

test("an uncertain missing result blocks replay and a fresh operation ID on that lane",async()=>{
 const value=plan(),remote=fixture(value);remote.failBefore();const directory=await journal();
 await expect(runNativeOperation(value,approval(value),remote.client,directory,1000)).rejects.toThrow();
 await expect(runNativeOperation(value,approval(value),remote.client,directory,1000)).rejects.toThrow();
 const replacement={...value,operationId:"c".repeat(32)};
 await expect(runNativeOperation(replacement,approval(replacement),remote.client,directory,1000)).rejects.toThrow();
 expect(remote.writes()).toBe(1);
 remote.makeVisible();
 expect((await runNativeOperation(replacement,approval(replacement),remote.client,directory,1000)).resourceId).toBe("created-record");
 expect(remote.writes()).toBe(1);
});

test("approval expiry is rechecked after waiting for provider access",async()=>{
 const value=plan(),remote=fixture(value);let clock=1000;
 remote.client.context=async()=>{clock=2001;return remote.context;};
 await expect(runNativeOperation(value,approval(value),remote.client,await journal(),()=>clock)).rejects.toThrow();
 expect(remote.writes()).toBe(0);
});

test("each later stage binds its parent, artifact, roster and inactive distribution settings",async()=>{
 const fingerprint=Array(32).fill("AB").join(":"),hash="d".repeat(64);
 const cases:NativeProviderPlan[]=[
  {...plan(),action:"create-group",beforeSha256:hash,target:{parentId:"app-record",name:"Private test",internalOnly:true,automaticDistribution:false,publicLink:false,memberIds:[]}},
  {...plan(),action:"sign-archive",beforeSha256:hash,target:{recordId:"app-record",parentId:"profile-record",certificateFingerprint:fingerprint,entitlementsSha256:hash,profileExpiresUnix:3000},artifact:{kind:"archive",appId:"dev.mychef.app",sourceCommit:plan().sourceCommit,sha256:hash,bundleSha256:hash}},
  {...plan(),action:"assign-build",beforeSha256:hash,target:{recordId:"group-record",parentId:"app-record",buildId:"build-record",internalOnly:true,automaticDistribution:false,publicLink:false,memberIds:["test-user"],assignedGroupIds:["group-record"],signingFingerprints:[fingerprint]},artifact:{kind:"ipa",appId:"dev.mychef.app",sourceCommit:plan().sourceCommit,sha256:hash,bundleSha256:hash}},
  {...plan(),provider:"google",action:"initial-play-upload",beforeSha256:hash,target:{recordId:"play-record",track:"internal",releaseStatus:"draft",signingMode:"play-app-signing",uploadCertificateFingerprint:fingerprint,version:"0.1.0",build:1},artifact:{kind:"aab",appId:"dev.mychef.app",sourceCommit:plan().sourceCommit,sha256:hash,bundleSha256:hash}},
 ];
 for(const value of cases){
  expect(nativeOperationDigest(value)).toMatch(/^[a-f0-9]{64}$/);
  const remote=fixture(value);let created=false;
  remote.client.find=async()=>created?[{provider:value.provider,accountId:value.accountId,appId:value.appId,environment:"preview",action:value.action,resourceId:String(value.target.recordId??"created-group"),target:value.target,artifactSha256:value.artifact?.sha256}]:[];
  remote.client.before=async()=>({provider:value.provider,accountId:value.accountId,environment:"preview",resourceId:String(value.target.recordId??value.target.parentId),appId:value.appId,sha256:hash});
  remote.client.artifact=async()=>value.artifact!;
  remote.client.apply=async()=>{created=true;};
  expect((await runNativeOperation(value,approval(value),remote.client,await journal(),1000)).appId).toBe("dev.mychef.app");
  const wrong=structuredClone(value);wrong.target.recordId="other-record";
  await expect(runNativeOperation(wrong,approval(value),remote.client,await journal(),1000)).rejects.toThrow();
  if(value.artifact){
   created=false;remote.client.artifact=async()=>({...value.artifact!,sha256:"e".repeat(64)});
   await expect(runNativeOperation(value,approval(value),remote.client,await journal(),1000)).rejects.toThrow();
   expect(created).toBe(false);
  }
 }
 for(const [action,target] of [
  ["create-group",{parentId:"app-record",name:"Private test",internalOnly:true,automaticDistribution:true,publicLink:false,memberIds:[]}],
  ["set-testers",{recordId:"group-record",parentId:"app-record",internalOnly:false,automaticDistribution:false,publicLink:false,memberIds:["test-user"]}],
  ["initial-play-upload",{recordId:"play-record",track:"production",releaseStatus:"draft",signingMode:"play-app-signing",uploadCertificateFingerprint:fingerprint,version:"0.1.0",build:1}],
 ] as const)expect(()=>nativeOperationDigest({...plan(),action,target,beforeSha256:hash})).toThrow();
});

test("provider drift, before-state mismatch and concurrent writes fail before mutation",async()=>{
 for(const mode of ["account-drift","before-drift","lock"]){
  const value={...plan(),action:"configure-capability",beforeSha256:"d".repeat(64),target:{recordId:"bundle-record",capability:"PUSH_NOTIFICATIONS",enabled:true}};
  const remote=fixture(value),directory=await journal();let calls=0,release!:()=>void;
  const waiting=new Promise<void>(resolve=>{release=resolve;});
  remote.client.context=async()=>({...remote.context,accountId:mode==="account-drift" && ++calls>1?"other-team":value.accountId});
  remote.client.before=async()=>{if(mode==="lock")await waiting;return {provider:value.provider,accountId:value.accountId,environment:"preview",resourceId:"bundle-record",appId:value.appId,sha256:(mode==="before-drift"?"e":"d").repeat(64)};};
  if(mode==="lock"){
   const first=runNativeOperation(value,approval(value),remote.client,directory,1000);
   // The first holder reaches before-state readback only after acquiring the lock.
   let reached=false;const before=remote.client.before;remote.client.before=async p=>{reached=true;return before(p);};
   for(let i=0;i<100 && !reached;i++)await new Promise(resolve=>setTimeout(resolve,1));
   expect(reached).toBe(true);
   await expect(runNativeOperation(value,approval(value),remote.client,directory,1000)).rejects.toThrow();
   release();await expect(first).rejects.toThrow(); // Fixture receipt is the wrong resource; no replay.
   expect(remote.writes()).toBe(1);
  }else{
   await expect(runNativeOperation(value,approval(value),remote.client,directory,1000)).rejects.toThrow();
   expect(remote.writes()).toBe(0);
  }
 }
});

test("Apple app creation binds Limited Access and the complete effective user roster", async () => {
  const baseline = plan();
  const value = {
    ...baseline,
    target: { ...baseline.target, userAccess: "limited", appUserIds: ["account-holder"] },
  };
  const consent = approval(value);
  for (const target of [
    { name: "MyChef", sku: "mychef-app-ios", platform: "IOS" },
    { ...value.target, userAccess: "full" },
    { ...value.target, appUserIds: [] },
  ]) {
    expect(() => nativeOperationDigest({ ...value, target })).toThrow();
  }
  const widened = { ...value, target: { ...value.target, appUserIds: ["account-holder", "unexpected-user"] } };
  const remote = fixture(value);
  await expect(runNativeOperation(widened, consent, remote.client, await journal(), 1000)).rejects.toThrow();
  expect(remote.writes()).toBe(0);
  const mismatchedReadback = fixture(widened);
  await expect(runNativeOperation(value, consent, mismatchedReadback.client, await journal(), 1000)).rejects.toThrow();
  expect(mismatchedReadback.writes()).toBe(1);
});
