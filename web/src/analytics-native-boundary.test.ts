import { test, expect } from "bun:test";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
test("Android forwards the original analytics revision through its real Java boundary", async () => {
  const dir = await mkdtemp(join(tmpdir(), "mychef-analytics-java-"));
  try {
    const file = join(dir, "AnalyticsBoundary.java");
    await writeFile(
      file,
      `import java.util.*;import com.gkldevelopment.mychef.preview.NativeBoundary;public class AnalyticsBoundary {public static void main(String[]args){var source=new HashMap<String,String>();source.put("X-MyChef-Analytics-Revision","17");source.put("Cookie","forbidden");var safe=NativeBoundary.headers(source);if(!"17".equals(safe.get("x-mychef-analytics-revision"))||safe.containsKey("cookie"))throw new AssertionError();}}`,
    );
    const compile = Bun.spawnSync([
      "javac",
      "-d",
      dir,
      resolve(
        import.meta.dir,
        "../../mobile/android/app/src/main/java/com/gkldevelopment/mychef/preview/NativeBoundary.java",
      ),
      file,
    ]);
    expect(compile.exitCode).toBe(0);
    expect(
      Bun.spawnSync(["java", "-cp", dir, "AnalyticsBoundary"]).exitCode,
    ).toBe(0);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
