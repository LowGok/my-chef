import { createClient } from "@hey-api/openapi-ts";
import { mkdtemp, readFile, rm, writeFile, rename } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const web = dirname(fileURLToPath(import.meta.url));
const input = resolve(process.argv[2] ?? join(web, "../api/openapi.yaml"));
const output = resolve(process.argv[3] ?? join(web, "src/api.generated.ts"));
const directory = await mkdtemp(join(tmpdir(), "mychef-api-"));
try {
  const spec = Bun.YAML.parse(await readFile(input, "utf8")) as {
    components: { schemas: Record<string, unknown> };
  };
  const names = Object.keys(spec.components.schemas);
  if (!names.length || names.some((name) => !/^[A-Za-z_$][\w$]*$/.test(name))) {
    throw new Error("API schema names must be valid TypeScript identifiers");
  }
  await createClient({
    input,
    output: directory,
    plugins: ["@hey-api/typescript"],
  });
  const types = await readFile(join(directory, "types.gen.ts"), "utf8");
  const compatibility = `\nexport interface components {\n  schemas: {\n${names.map((name) => `    "${name}": ${name};`).join("\n")}\n  };\n}\n`;
  // Keep the existing components["schemas"] API used by the application.
  // Publish only after parsing and generation both succeed.
  const publication = await mkdtemp(join(dirname(output), ".api-generated-"));
  const pending = join(publication, "generated.ts");
  try {
    await writeFile(pending, types + compatibility);
    await rename(pending, output);
  } finally {
    await rm(publication, { recursive: true, force: true });
  }
} finally {
  await rm(directory, { recursive: true, force: true });
}
