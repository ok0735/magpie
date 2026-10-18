#!/usr/bin/env node
// push-profile — merge profiles.json into the task-router plugin's options
// in magpie's plugins.json (with a timestamped backup next to it).
//
// Usage:
//   node push-profile.mjs [profiles.json] [--enable] [--mode profiles|groups]
//
// plugins.json is found the same way magpie finds it (internal/appdir):
// $XDG_CONFIG_HOME/magpie, else ~/.config/magpie. MAGPIE_CONFIG_DIR
// overrides for testing.

import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const profilesPath = args.find(a => !a.startsWith("--")) || path.join(here, "..", "profiles.json");
const enable = args.includes("--enable");
const modeIdx = args.indexOf("--mode");
const mode = modeIdx >= 0 ? args[modeIdx + 1] : null;

function configDir() {
  if (process.env.MAGPIE_CONFIG_DIR) return process.env.MAGPIE_CONFIG_DIR;
  const xdg = process.env.XDG_CONFIG_HOME;
  if (xdg && path.isAbsolute(xdg)) return path.join(xdg, "magpie");
  return path.join(os.homedir(), ".config", "magpie");
}

const cfgDir = configDir();
const pluginsPath = path.join(cfgDir, "plugins.json");
if (!fs.existsSync(pluginsPath)) {
  console.error("error: no plugins.json at " + pluginsPath);
  console.error("(magpie keeps it there; set MAGPIE_CONFIG_DIR to override for testing)");
  process.exit(1);
}

const profiles = JSON.parse(fs.readFileSync(profilesPath, "utf8"));
if (!profiles.models || Object.keys(profiles.models).length === 0) {
  console.error("error: " + profilesPath + " has no models — run run-profile.mjs first");
  process.exit(1);
}

const backup = pluginsPath + ".bak-" + new Date().toISOString().replace(/[:.]/g, "-");
fs.copyFileSync(pluginsPath, backup);

const list = JSON.parse(fs.readFileSync(pluginsPath, "utf8"));
const plugins = list.plugins || [];
let entry = null;
for (const e of plugins) {
  const spec = String(e.spec || "");
  if (spec.indexOf("task-router") !== -1) { entry = e; break; }
}
if (!entry) {
  console.error("error: no task-router entry in " + pluginsPath);
  console.error("add the plugin first: magpie plugin add <path to packages-local/task-router>");
  process.exit(1);
}

entry.options = entry.options && typeof entry.options === "object" ? entry.options : {};
entry.options.profiles = profiles;             // the middleware reads ctx.options.profiles
if (mode) entry.options.mode = mode;
if (enable) entry.options.enabled = true;

fs.writeFileSync(pluginsPath, JSON.stringify(list, null, 2) + "\n");
console.log("updated " + pluginsPath);
console.log("backup " + backup);
console.log("models profiled: " + Object.keys(profiles.models).join(", "));
console.log("mode: " + (entry.options.mode || "unchanged") + ", enabled: " + entry.options.enabled);
console.log("reload the middleware (restart magpie, or toggle the plugin in the Plugins page) to pick it up.");
