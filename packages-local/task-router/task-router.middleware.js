// task-router — a gateway middleware that picks, per request, the model or
// routing group best fitted to what the request asks. Two modes:
//
//   groups (default)   classify the request and rewrite body.model to
//                      "group/<id>" from a hand-set mapping — cheap capable
//                      models for code, strong ones for hard reasoning,
//                      Chinese-optimised ones for Chinese chat.
//   profiles           pick a concrete model by measured capability and
//                      cost: options.profiles carries per-model category
//                      scores (and optional CNY cost) produced by
//                      profile/run-profile.mjs; the middleware filters by
//                      a minimum score, then prefers higher quality, then
//                      cheaper, then higher overall.
//
// Why a rewritten model picks the pool: the gateway runs this hook on the
// agent's request BEFORE it resolves the model to a provider or group
// (serveAgent -> middleware chain -> serve; provider.GroupPrefix is
// "group/"). This is the same trick the model-map middleware uses.
//
// Inert by default: nothing is rewritten until options.enabled is true and
// the chosen target exists. An explicit "group/..." model the agent already
// asked for is left alone unless overrideExplicitGroups is set. A hook that
// throws leaves the body as it was: routing never breaks a request.

// ---- request classification ---------------------------------------------
// Built-in categories, tried in order per message. In groups mode a
// category only routes when options.groups maps it; in profiles mode the
// category picks which score column to rank by.

var CODE_RE = /(\x60{3})|\b(function|class|def|fn|impl|import|export|package|const|let|var|return)\b|=>|\b(if|for|while|switch)\s*\(|Traceback|Exception|stack trace|error:|fatal:|warning:|\b(npm|pnpm|yarn|cargo)\b|\bgit\s+(diff|commit|push|pull|rebase|merge)\b|\.(js|ts|tsx|jsx|py|go|rs|java|kt|c|cpp|h|hpp|cs|rb|php|swift|json|yaml|yml|toml|css|scss|html|sql|sh|ps1)\b|\b(bug|fix|refactor|compile|panic)\b|\b(TypeError|ReferenceError|SyntaxError)\b|\b(stdout|stderr)\b/i;

var REASON_RE = /\b(prove|proof|derive|theorem|lemma|mathematically|step[- ]by[- ]step|reason|logical|logic|puzzle|riddle|probability|optimi[sz]e)\b|证明|推导|定理|为什么|逻辑|数学|求解|推理/i;

var CJK_RE = /[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]/g;
var LATIN_RE = /[A-Za-z]/g;
var GROUP_PREFIX = "group/";
var MAX_SCAN = 6000;

function trim(s) { return String(s).replace(/^\s+|\s+$/g, ""); }

// textOf flattens one message's content to text: a string, or OpenAI's /
// Anthropic's part arrays (text blocks, tool_result blocks, nested content).
function textOf(v) {
  if (v === null || v === undefined) return "";
  var t = typeof v;
  if (t === "string") return v;
  if (t !== "object") return "";
  if (Object.prototype.toString.call(v) === "[object Array]") {
    var parts = [];
    for (var i = 0; i < v.length; i++) {
      var s = textOf(v[i]);
      if (s) parts.push(s);
    }
    return parts.join("\n");
  }
  if (typeof v.text === "string") return v.text;
  if (v.content !== undefined && v.content !== null) return textOf(v.content);
  return "";
}

// userTexts gathers the body's last few user messages, newest first, each
// capped, so a classification holds over a turn rather than wobbling on one
// short line. Only role "user" counts; system prompts say nothing of the
// task.
function userTexts(body, lookback) {
  var msgs = body && body.messages;
  if (!msgs || typeof msgs.length !== "number") return [];
  var out = [];
  for (var i = msgs.length - 1; i >= 0 && out.length < lookback; i--) {
    var m = msgs[i];
    if (!m || m.role !== "user") continue;
    var s = textOf(m.content);
    if (s.length > MAX_SCAN) s = s.slice(0, MAX_SCAN);
    if (s) out.push(s);
  }
  return out;
}

// cjkRatio is CJK chars over CJK plus ASCII letters: a Chinese name dropped
// in English prose stays low, a Chinese sentence crosses the line.
function cjkRatio(t) {
  CJK_RE.lastIndex = 0;
  LATIN_RE.lastIndex = 0;
  var c = 0, l = 0, m;
  while ((m = CJK_RE.exec(t)) !== null) c++;
  while ((m = LATIN_RE.exec(t)) !== null) l++;
  var total = c + l;
  if (total === 0) return 0;
  return c / total;
}

// classify returns the first category one of the recent messages matches,
// newest message first; "" when none does (a general request).
function classify(texts, o) {
  var threshold = typeof o.cjkRatio === "number" ? o.cjkRatio : 0.25;
  for (var i = 0; i < texts.length; i++) {
    var t = texts[i];
    if (CODE_RE.test(t)) return "code";
    if (REASON_RE.test(t)) return "reasoning";
    if (cjkRatio(t) >= threshold) return "chinese";
  }
  return "";
}

// ---- target pickers ------------------------------------------------------

// pickByGroups is the groups mode: a category to "group/<id>" mapping, with
// defaultGroup for requests no category matched.
function pickByGroups(o, cat) {
  var groups = (o.groups && typeof o.groups === "object") ? o.groups : {};
  var t = cat ? groups[cat] : "";
  if ((!t || typeof t !== "string") && !cat && typeof o.defaultGroup === "string") {
    t = o.defaultGroup;
  }
  if (!t || typeof t !== "string") return "";
  return GROUP_PREFIX + String(t).replace(new RegExp("^" + GROUP_PREFIX), "");
}

// pickByProfiles is the profiles mode: rank the profiled models by their
// score in this request's category (overall as the fallback column), keep
// those at or above minScore, then prefer higher quality, then cheaper
// (costPerRequestCNY, CNY per request as measured by the profiler), then
// higher overall. Returns the catalog model id, or "" when nothing
// qualifies.
function pickByProfiles(o, cat) {
  var p = o.profiles;
  if (!p || typeof p !== "object" || !p.models) return "";
  var min = (typeof o.minScore === "number") ? o.minScore : 0.5;
  var best = "", bestQ = -1, bestC = Infinity, bestOv = -1;
  for (var id in p.models) {
    if (!Object.prototype.hasOwnProperty.call(p.models, id)) continue;
    var m = p.models[id];
    if (!m || typeof m !== "object") continue;
    var q = -1;
    if (m.scores && typeof m.scores[cat] === "number") q = m.scores[cat];
    else if (typeof m.overall === "number") q = m.overall;
    if (q < 0 || q < min) continue;
    var c = (typeof m.costPerRequestCNY === "number") ? m.costPerRequestCNY : Infinity;
    var ov = (typeof m.overall === "number") ? m.overall : 0;
    if (best === "" ||
        q > bestQ + 1e-9 ||
        (q >= bestQ - 1e-9 && (c < bestC * (1 - 1e-12) ||
          (c <= bestC * (1 + 1e-12) && ov > bestOv)))) {
      best = id; bestQ = q; bestC = c; bestOv = ov;
    }
  }
  return best;
}

// ---- the hook ------------------------------------------------------------

// onRequest is the gateway hook. body is the parsed request JSON; what it
// returns is what the gateway goes on with, and undefined leaves it as it
// was. ctx carries {protocol, model, stream, path, agent, options, state}.
export async function onRequest(body, ctx) {
  try {
    if (!body || typeof body !== "object") return;
    var o = (ctx && ctx.options) ? ctx.options : {};
    if (o.enabled !== true) return;

    if (typeof body.model !== "string" || body.model === "") return;
    var cur = trim(body.model);
    if (o.overrideExplicitGroups !== true && cur.indexOf(GROUP_PREFIX) === 0) return;

    var lookback = (typeof o.lookbackMessages === "number" && o.lookbackMessages > 0)
      ? Math.floor(o.lookbackMessages) : 3;
    var texts = userTexts(body, lookback);
    if (texts.length === 0) return;
    var cat = classify(texts, o);

    var target = "";
    if (o.mode === "profiles") target = pickByProfiles(o, cat);
    if (!target) target = pickByGroups(o, cat);
    if (!target || typeof target !== "string") return;
    if (target === cur) return;

    body.model = target;
    return body;
  } catch (e) {
    return; // never break a request: the body goes on as it was
  }
}
