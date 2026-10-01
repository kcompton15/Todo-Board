import { readFileSync } from "node:fs";
import { Script } from "node:vm";
import { spawnSync } from "node:child_process";

const html = readFileSync(
  new URL("../webui/board.html", import.meta.url),
  "utf8",
);
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
if (!scripts.length) throw new Error("No inline board JavaScript found");
for (const [, source] of scripts) {
  new Script(source); // Parse exactly the embedded browser code, without executing it.
  const result = spawnSync("deno", [
    "lint",
    "--rules-exclude=no-window,no-window-prefix",
    "--ext=js",
    "-",
  ], { input: source, encoding: "utf8" });
  if (result.error) throw result.error;
  process.stdout.write(result.stdout);
  process.stderr.write(result.stderr);
  if (result.status !== 0) process.exit(result.status ?? 1);
}
