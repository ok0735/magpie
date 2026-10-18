#!/usr/bin/env node
// run-profile — capability profiler for the task-router middleware.
//
// Sends the eval questions to each candidate model THROUGH the magpie
// gateway (the same path agents take), grades the answers, and writes
// profiles.json the middleware reads via its options.
//
// Usage:
//   node run-profile.mjs --models "provider/model-a,provider/model-b" \
//        --key sk-magpie-key-... \
//        [--gateway http://127.0.0.1:3425] \
//        [--out ../profiles.json] \
//        [--prices '{"provider/model-a":{"in":2,"out":8}}'] \
//        [--timeout 90] [--max-tokens 512] [--tag my-pool]
//
// Prices are CNY per 1M tokens (prompt/completion); without them the
// profile carries no cost and the middleware ranks on quality alone.
// Eval traffic is real traffic: models=x questions=24 requests, billed.

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { grade } from "./grade.mjs";

// The only real dependency is Node >= 18: built-in fetch (and AbortController)
// is what talks to the gateway. Say so plainly instead of dying with a
// cryptic "fetch is not defined" on an old runtime.
const NODE_MAJOR = Number(process.versions.node.split(".")[0]);
if (NODE_MAJOR < 18) {
  console.error("error: Node.js >= 18 is required (built-in fetch); found " + process.versions.node);
  console.error("install a current Node from https://nodejs.org and run this again;");
  console.error("or copy a profiles.json from another machine and run push-profile.mjs.");
  process.exit(1);
}

const here = path.dirname(fileURLToPath(import.meta.url));

function arg(name, dflt) {
  const i = process.argv.indexOf("--" + name);
  if (i >= 0 && i + 1 < process.argv.length) return process.argv[i + 1];
  return dflt;
}
function fail(msg) { console.error("error: " + msg); process.exit(1); }

const modelsArg = arg("models", "");
const key = arg("key", process.env.MAGPIE_GATEWAY_KEY || "");
const gateway = arg("gateway", "http://127.0.0.1:3425").replace(/\\/+$/, "");
const out = arg("out", path.join(here, "..", "profiles.json"));
const timeout = Number(arg("timeout", "90")) * 1000;
const maxTokens = Number(arg("max-tokens", "512"));
const tag = arg("tag", "");
if (!modelsArg) fail("--models is required (comma-separated catalog ids, e.g. deepseek/deepseek-chat)");
if (!key) fail("--key is required (a gateway key, sk-magpie-key-...) — the gateway authenticates every call");

const models = modelsArg.split(",").map(s => s.trim()).filter(Boolean);
const prices = arg("prices", "") ? JSON.parse(arg("prices")) : null;

const set = JSON.parse(fs.readFileSync(path.join(here, "questions.json"), "utf8"));
const questions = set.questions;

async function ask(model, prompt) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeout);
  const started = Date.now();
  try {
    const res = await fetch(gateway + "/v1/chat/completions", {
      method: "POST",
      headers: { "content-type": "application/json", "authorization": "Bearer " + key },
      body: JSON.stringify({
        model: model,
        messages: [{ role: "user", content: prompt }],
        max_tokens: maxTokens,
        stream: false,
      }),
      signal: ctrl.signal,
    });
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error("HTTP " + res.status + " " + text.slice(0, 160));
    }
    const data = await res.json();
    const choice = data.choices && data.choices[0];
    const text = choice && choice.message && typeof choice.message.content === "string"
      ? choice.message.content : "";
    const usage = data.usage || {};
    return {
      text: text,
      seconds: (Date.now() - started) / 1000,
      promptTokens: usage.prompt_tokens || 0,
      completionTokens: usage.completion_tokens || 0,
    };
  } finally {
    clearTimeout(timer);
  }
}

function costCNY(model, pt, ct) {
  if (!prices || !prices[model]) return null;
  const p = prices[model] || {};
  const pin = typeof p.in === "number" ? p.in : null;
  const pout = typeof p.out === "number" ? p.out : null;
  if (pin === null && pout === null) return null;
  let sum = 0;
  if (pin !== null) sum += (pt / 1e6) * pin;
  if (pout !== null) sum += (ct / 1e6) * pout;
  return sum;
}

const report = {};
for (const model of models) {
  console.log("== " + model + " ==");
  const detail = {};
  const catSum = {}, catN = {};
  let pt = 0, ct = 0, cost = 0, costed = 0, seconds = 0, errors = 0;
  for (const q of questions) {
    let r = null;
    try { r = await ask(model, q.prompt); }
    catch (e) {
      errors++;
      detail[q.id] = { score: 0, error: String(e.message || e).slice(0, 120) };
      console.log("  " + q.id + ": ERROR " + String(e.message || e).slice(0, 80));
      continue;
    }
    const s = grade(q, r.text);
    detail[q.id] = { score: s, seconds: +r.seconds.toFixed(1) };
    catSum[q.category] = (catSum[q.category] || 0) + s;
    catN[q.category] = (catN[q.category] || 0) + 1;
    pt += r.promptTokens; ct += r.completionTokens; seconds += r.seconds;
    const c = costCNY(model, r.promptTokens, r.completionTokens);
    if (c !== null) { cost += c; costed++; }
    console.log("  " + q.id + ": " + (s ? "ok  " : "miss") + "  " + r.seconds.toFixed(1) + "s");
  }
  const scores = {};
  for (const cat of set.categories) {
    if (catN[cat]) scores[cat] = +(catSum[cat] / catN[cat]).toFixed(3);
  }
  const cats = Object.keys(scores);
  const overall = cats.length
    ? +(cats.reduce((a, c) => a + scores[c], 0) / cats.length).toFixed(3) : 0;
  const done = questions.length - errors;
  report[model] = {
    scores: scores,
    overall: overall,
    samples: done,
    errors: errors,
    avgSeconds: done ? +(seconds / done).toFixed(1) : null,
    tokens: { prompt: pt, completion: ct },
  };
  if (costed > 0) report[model].costPerRequestCNY = +(cost / costed).toFixed(5);
  console.log("  => " + JSON.stringify(report[model]));
}

const profiles = {
  version: 1,
  generatedAt: new Date().toISOString(),
  gateway: gateway,
  tag: tag,
  questions: questions.length,
  models: report,
};
fs.writeFileSync(out, JSON.stringify(profiles, null, 2) + "\n");
console.log("\nwrote " + out);
console.log("next: node push-profile.mjs \"" + out + "\" (merges it into the plugin's options)");
