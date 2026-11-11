import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
const root = join(import.meta.dir, "../web/dist");
const page = '<!doctype html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>MyChef</title></head><body><main><h1>MyChef</h1><p><a href="/app/">Open application</a></p></main></body></html>';
await mkdir(root, { recursive: true });
await writeFile(join(root, "index.html"), page);
