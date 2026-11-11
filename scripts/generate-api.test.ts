import { expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const web = new URL("../web/", import.meta.url).pathname;
test("API generation is deterministic, type-only and preserves schema names", () => {
  const directory = mkdtempSync(join(tmpdir(), "mychef-api-test-"));
  try {
    const output = join(directory, "api.ts");
    writeFileSync(`${output}.tmp`, "unrelated draft");
    const run = () =>
      spawnSync(
        process.execPath,
        ["generate.ts", "../api/openapi.yaml", output],
        { cwd: web, encoding: "utf8" },
      );
    const first = run();
    expect(first.status, first.stderr).toBe(0);
    expect(readFileSync(`${output}.tmp`, "utf8")).toBe("unrelated draft");
    const generated = readFileSync(output, "utf8");
    expect(generated).toBe(
      readFileSync(join(web, "src/api.generated.ts"), "utf8"),
    );
    expect(run().status).toBe(0);
    expect(readFileSync(output, "utf8")).toBe(generated);
    expect(generated).toContain("export interface components");
    for (const name of [
      "Profile",
      "Recipe",
      "ShoppingList",
      "FavoriteWeek",
      "PersonalRecipe",
      "PersonalRecipeContent",
      "PersonalSaveInput",
      "PersonalCopy",
      "PersonalPermissions",
    ])
      expect(generated).toContain(`"${name}": ${name}`);
    expect(generated).not.toContain("export const");
    const input = join(directory, "invalid.yaml");
    writeFileSync(input, "invalid: [");
    const invalid = spawnSync(
      process.execPath,
      ["generate.ts", input, output],
      { cwd: web },
    );
    expect(invalid.status).not.toBe(0);
    expect(readFileSync(output, "utf8")).toBe(generated);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
