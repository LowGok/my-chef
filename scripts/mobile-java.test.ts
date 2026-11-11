import { expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";

test("Android boundary and credential encryption enforce the actual Java contract on JVM", async () => {
  const root = resolve(import.meta.dir, "..");
  const source = join(
    root,
    "mobile/android/app/src/main/java/com/gkldevelopment/mychef/preview",
  );
  const output = await mkdtemp(join(tmpdir(), "mychef-native-java-"));
  try {
    const harness = join(output, "NativeContract.java");
    await writeFile(
      harness,
      `
import com.gkldevelopment.mychef.preview.*;
import java.util.*;
import java.net.*;
import javax.crypto.*;
public class NativeContract {
  static void reject(Runnable action) { try { action.run(); } catch (IllegalArgumentException expected) { return; } throw new AssertionError("Accepted unsafe input"); }
  public static void main(String[] args) throws Exception {
    URI origin = NativeBoundary.origin("https://fixture-node.tailtest.ts.net:8977");
    for (String invalid : new String[] {"http://localhost", "https://foreign.test", "https://user:secret@localhost", "https://localhost/", "https://localhost?secret", "https://localhost#secret"}) reject(() -> NativeBoundary.origin(invalid));
    for (String invalid : new String[] {"https://foreign.test/api/native/v1/session", "//foreign.test/api/native/v1/session", "/api/native/v1/../session", "/api/native/v1/%2e%2e/session", "/api/v1/session", "/api/native/v1/session#secret"}) reject(() -> NativeBoundary.target(origin, invalid));
    if (!NativeBoundary.target(origin,"/api/native/v1/session").toString().equals(origin+"/api/native/v1/session")) throw new AssertionError();
    Map<String,String> headers = new HashMap<>(); headers.put("Cookie","foreign"); headers.put("Origin","foreign"); headers.put("Authorization","Bearer "+"a".repeat(64)); headers.put("Idempotency-Key","same");
    Map<String,String> safe = NativeBoundary.headers(headers);
    if (safe.size()!=2 || !safe.get("idempotency-key").equals("same")) throw new AssertionError();
    headers.put("Authorization","Bearer other"); reject(() -> NativeBoundary.headers(headers));
    NativeBoundary.publicPhoto("https://res.cloudinary.com/fixture/image/upload/dish.jpg");
    NativeBoundary.externalLink("https://recipes.example.test/carrot-soup");
    NativeBoundary.externalLink("mailto:admin@example.invalid");
    for (String invalid : new String[] {"http://res.cloudinary.com/photo.jpg", "https://foreign.test/photo.jpg", "https://user:secret@res.cloudinary.com/photo.jpg", "https://res.cloudinary.com:8443/photo.jpg", "https://res.cloudinary.com/photo.jpg#secret"}) reject(() -> NativeBoundary.publicPhoto(invalid));
    for (String invalid : new String[] {"file:///private", "intent://foreign", "javascript:alert(1)", "https://user:secret@foreign.test/", "mailto:admin@example.invalid?bcc=foreign@example.test"}) reject(() -> NativeBoundary.externalLink(invalid));
    String accountLink=origin+"/app/reset-password#"+"a".repeat(43);
    if (!NativeBoundary.accountLink(origin,accountLink).equals(accountLink)) throw new AssertionError();
    for (String invalid : new String[] {origin+"/app/signup?token=private#"+"a".repeat(43),origin+"/app/%73ignup#"+"a".repeat(43),"https://foreign.test/app/signup#"+"a".repeat(43),origin+"/app/signup#short"}) reject(() -> NativeBoundary.accountLink(origin,invalid));
    NativeRuntimeState runtime = new NativeRuntimeState();
    runtime.link(accountLink,1000); if (!accountLink.equals(runtime.takeLink(1001)) || runtime.takeLink(1002)!=null) throw new AssertionError();
    runtime.link(accountLink,1000); if (runtime.takeLink(301001)!=null) throw new AssertionError("Expired link retained");
    NativeBoundary.exportName("mychef-data-"+"a".repeat(32)+".zip");
    reject(() -> NativeBoundary.exportName("../private.zip"));
    byte[] zip = new byte[22]; zip[0]=0x50; zip[1]=0x4b; zip[2]=0x05; zip[3]=0x06; NativeBoundary.zip(zip);
    reject(() -> NativeBoundary.zip(new byte[22]));
    KeyGenerator generator = KeyGenerator.getInstance("AES"); generator.init(256); var key = generator.generateKey();
    String secret = "b".repeat(64); byte[] first = RefreshVault.encrypt(key,secret), second = RefreshVault.encrypt(key,secret);
    if (Arrays.equals(first,second) || !RefreshVault.decrypt(key,first).equals(secret) || new String(first,java.nio.charset.StandardCharsets.ISO_8859_1).contains(secret)) throw new AssertionError();
    first[15] ^= 1;
    try { RefreshVault.decrypt(key,first); throw new AssertionError("Tamper accepted"); } catch (java.security.GeneralSecurityException expected) {}
    try { RefreshVault.decrypt(generator.generateKey(),second); throw new AssertionError("Foreign key accepted"); } catch (java.security.GeneralSecurityException expected) {}
    String instance = "a".repeat(32);
    byte[] shopping = ShoppingCipher.encrypt(key,"1",instance,"private ordinary shopping");
    if (!ShoppingCipher.decrypt(key,"1",instance,shopping).equals("private ordinary shopping")) throw new AssertionError();
    try { ShoppingCipher.decrypt(key,"2",instance,shopping); throw new AssertionError("Foreign owner accepted"); } catch (java.security.GeneralSecurityException expected) {}
    try { ShoppingCipher.decrypt(key,"1","b".repeat(32),shopping); throw new AssertionError("Foreign incarnation accepted"); } catch (java.security.GeneralSecurityException expected) {}
    shopping[20] ^= 1;
    try { ShoppingCipher.decrypt(key,"1",instance,shopping); throw new AssertionError("Shopping tamper accepted"); } catch (java.security.GeneralSecurityException expected) {}
    System.out.println("native JVM boundary and ciphertext checks passed");
  }
}`,
    );
    const compile = Bun.spawnSync(
      [
        "javac",
        "--release",
        "17",
        "-d",
        output,
        join(source, "NativeBoundary.java"),
        join(source, "NativeRuntimeState.java"),
        join(source, "RefreshVault.java"),
        join(source, "ShoppingCipher.java"),
        harness,
      ],
      { env: { PATH: process.env.PATH, LANG: "C.UTF-8" } },
    );
    expect(compile.exitCode, compile.stderr.toString()).toBe(0);
    const run = Bun.spawnSync(["java", "-cp", output, "NativeContract"], {
      env: { PATH: process.env.PATH, LANG: "C.UTF-8" },
    });
    expect(run.exitCode, run.stderr.toString()).toBe(0);
    expect(run.stdout.toString()).toContain("checks passed");
  } finally {
    await rm(output, { recursive: true, force: true });
  }
}, 30_000);

test("native probe maps visible CSS controls into the focused WebView screen", async () => {
  const output = await mkdtemp(join(tmpdir(), "mychef-native-touch-"));
  try {
    const harness = join(output, "TouchPointContract.java");
    await writeFile(
      harness,
      `
import com.gkldevelopment.mychef.testing.NativeTouchPoint;
import java.util.Arrays;
public class TouchPointContract {
  static void reject(Runnable work) { try { work.run(); } catch (IllegalArgumentException expected) { return; } throw new AssertionError("Unsafe touch point accepted"); }
  public static void main(String[] args) {
    int[] point = NativeTouchPoint.locate(new double[]{180,220,0,120,360,400}, new int[]{0,72},1080,1200,true);
    if (!Arrays.equals(point,new int[]{540,372})) throw new AssertionError("Viewport offset or screen origin ignored");
    point = NativeTouchPoint.locate(new double[]{180,220,0,120,360,400},new int[]{-8,-20},1080,1200,true);
    if (!Arrays.equals(point,new int[]{532,280})) throw new AssertionError("A clipped view can still contain a visible control");
    point = NativeTouchPoint.locate(new double[]{90,120,30,20,180,200},new int[]{8,50},1080,1400,true);
    if (!Arrays.equals(point,new int[]{368,650})) throw new AssertionError("Zoomed viewport mapped incorrectly");
    reject(() -> NativeTouchPoint.locate(new double[]{180,220,0,120,360,400},new int[]{0,72},1080,1200,false));
    reject(() -> NativeTouchPoint.locate(new double[]{180,20,0,120,360,400},new int[]{0,72},1080,1200,true));
    reject(() -> NativeTouchPoint.locate(new double[]{180,220,0,120,0,400},new int[]{0,72},1080,1200,true));
    reject(() -> NativeTouchPoint.locate(new double[]{Double.NaN,220,0,120,360,400},new int[]{0,72},1080,1200,true));
    reject(() -> NativeTouchPoint.locate(new double[]{5,220,0,120,360,400},new int[]{-25,72},1080,1200,true));
    System.out.println("native touch checks passed");
  }
}`,
    );
    const compile = Bun.spawnSync([
      "javac",
      "--release",
      "17",
      "-d",
      output,
      resolve(import.meta.dir, "native-android/NativeTouchPoint.java"),
      harness,
    ]);
    expect(compile.exitCode, compile.stderr.toString()).toBe(0);
    const run = Bun.spawnSync(["java", "-cp", output, "TouchPointContract"]);
    expect(run.exitCode, run.stderr.toString()).toBe(0);
    expect(run.stdout.toString()).toContain("native touch checks passed");
  } finally {
    await rm(output, { recursive: true, force: true });
  }
}, 30_000);
