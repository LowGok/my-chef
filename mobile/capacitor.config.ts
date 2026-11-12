import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { CapacitorConfig } from "@capacitor/cli";

// Preparation validates the explicit private endpoint and hashes this config
// together with the embedded assets. A missing preparation must stop the CLI.
const config: CapacitorConfig = JSON.parse(
  readFileSync(resolve(".generated/runtime-config.json"), "utf8"),
);
export default config;
