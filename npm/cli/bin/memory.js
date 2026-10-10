#!/usr/bin/env node

import { existsSync, realpathSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { runChild, settle } from "../lib/child.js";
import { selectTarget } from "../lib/targets.js";
import { updateNpmInstall } from "../lib/update.js";

const require = createRequire(import.meta.url);

async function main() {
  const packageRoot = realpathSync(path.join(path.dirname(fileURLToPath(import.meta.url)), ".."));
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === "update") {
    settle(await updateNpmInstall({ packageRoot }));
    return;
  }

  const target = selectTarget(process.platform, process.arch);
  let platformRoot;
  try {
    platformRoot = path.dirname(require.resolve(`${target.alias}/package.json`));
  } catch {
    throw new Error(
      `Missing optional dependency ${target.alias}. Reinstall Memory with: ` +
        "npm install --global --include=optional @jaywanx/memory-cli@latest",
    );
  }

  const binary = path.join(platformRoot, target.binary);
  if (!existsSync(binary)) {
    throw new Error(
      `Memory native binary is missing for ${target.id}. Reinstall with: ` +
        "npm install --global --include=optional @jaywanx/memory-cli@latest",
    );
  }
  settle(await runChild(binary, args));
}

try {
  await main();
} catch (error) {
  process.stderr.write(`memory: ${error.message}\n`);
  process.exitCode = 1;
}
