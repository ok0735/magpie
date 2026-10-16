// The chat window beside the Providers page: paste a model name, a base URL
// and a key, and magpie adds the provider, fetches the models that key sees,
// tests every one of them and keeps the ones that answer — a model that lists
// a name but can't be called is taken out of the provider's picks. The run
// itself is served by agentchat.go, which streams every step back as one JSON
// object per line.
(() => {
  const $id = (id) => document.getElementById(id);
  const panel = $id("agentChatPanel");
  const openBtn = $id("agentChat");
  const body = $id("agentChatBody");
  const input = $id("agentChatInput");
  const sendBtn = $id("agentChatSend");
  const maxIn = $id("agentChatMax");
  const closeBtn = $id("agentChatClose");
  if (!panel || !openBtn || !input) return;

  // the page's language: its own (locale, set by i18n.js) first, since this
  // script may run before the page has set the tag it writes it to
  const langOf = () => String(
    (typeof locale === "string" && locale) ||
    (window.bootPrefs && window.bootPrefs.lang) ||
    document.documentElement.lang || navigator.language || "").toLowerCase();
  const WORDS = {
    zh: {
      title: "AI 助手",
      sub: "粘贴 模型名 / 网址 / key，或 MCP 配置",
      ph: "例如：\ngpt-5.2\nhttps://api.example.com/v1\nsk-xxxxxxxx\n\n或 MCP 配置：\n{\"mcpServers\": {\"名字\": {\"command\": \"…\"}}}",
      send: "发送", run: "正在处理…", done: "完成",
      max: "最多测试", unit: "个模型（0 = 全部）",
      base: "网址", models: "模型", name: "名称", key: "key",
      exist: "已有相同供应商 {name}（{id}），直接用它",
      preset: "网址匹配内置预设：{name}（{id}）",
      creating: "正在新建供应商…",
      created: "已新建供应商：{name}（{id}）", speaks: "协议",
      keys_: "它的 key 里补了 {n} 个新 key",
      listed: "拉取到 {n} 个模型",
      testing: "开始逐个测试 {n} 个模型…",
      keep: "保留：能用", drop: "已删除：能列出、不能调用",
      saved: "已保存：{id} 只保留 {n} 个能用的模型",
      result: "测试完成：能用 {n}，不能用 {m}",
      untested: "{n} 个未测试（上限），原样保留",
      http: "请求失败：{e}",
      mcps: "识别为 MCP 配置：{n} 个 server",
      mcpAdd: "已加入", mcpUpdate: "已覆盖", mcpSame: "已在库中",
      mcpDone: "已写入 magpie 资源库 › MCP。要给它，打开资源库 › MCP，勾选要用的 agent——现在还没有给任何 agent。",
      mcpNone: "MCP 配置里没有可用的 server",
      problems: {
        noparse: "没读懂粘贴的内容：请把 模型名、网址、key 一起发给我",
        nobase: "没找到网址：请把接口地址贴上来，例如 https://api.example.com/v1",
        nokey: "没找到 key：请把 key 贴上来（sk-… 或写「密钥：…」）",
        noprovider: "供应商没能建立：{detail}",
        notestable: "这个供应商的模型不能逐个测试（{detail}），已跳过测试与删除",
        nolist: "拉取模型列表失败：{detail}",
        nomodels: "这家供应商没有列出任何模型",
        allfail: "测试的 {n} 个模型全部不可用，未改动任何设置：供应商已保留，改好 key 或网址再发我一次",
        capped: "只测试了前 {count} 个模型，其余 {untested} 个原样保留",
        timeout: "等待太久，没测完的模型原样保留",
        nosave: "保存失败：{detail}",
        noapi: "没有哪个 API 应答（{detail}）；先按 OpenAI 兼容地址保存，测试时会再试",
      },
    },
    en: {
      title: "AI assistant",
      sub: "paste a model, a URL and a key — or an MCP config",
      ph: "for example:\ngpt-5.2\nhttps://api.example.com/v1\nsk-xxxxxxxx\n\nor an MCP config:\n{\"mcpServers\": {\"name\": {\"command\": \"…\"}}}",
      send: "Send", run: "Working…", done: "Done",
      max: "test at most", unit: "models (0 = all)",
      base: "URL", models: "models", name: "name", key: "key",
      exist: "already added as {name} ({id}), used as it is",
      preset: "the URL is the preset {name} ({id})",
      creating: "adding the provider…",
      created: "added the provider {name} ({id})", speaks: "APIs",
      keys_: "{n} more keys joined it",
      listed: "{n} models listed",
      testing: "testing {n} models, one by one…",
      keep: "kept: they answered", drop: "removed: listed, but not callable",
      saved: "saved: {id} serves only the {n} models that answered",
      result: "{n} answered, {m} didn't",
      untested: "{n} not tested (the cap), left as they were",
      http: "the request failed: {e}",
      mcps: "Read as an MCP config: {n} servers",
      mcpAdd: "added", mcpUpdate: "written over", mcpSame: "already there",
      mcpDone: "Written into magpie's library › MCP. To give it to an agent, open the library › MCP and tick the ones to use it — nothing has one yet.",
      mcpNone: "the MCP config names no server that can be used",
      problems: {
        noparse: "nothing readable: paste a model name, a URL and a key",
        nobase: "no base URL: paste the endpoint, e.g. https://api.example.com/v1",
        nokey: "no key: paste the key too (sk-…, or \"Key: …\")",
        noprovider: "the provider wasn't added: {detail}",
        notestable: "this provider's models can't be tested one by one ({detail}); testing and removal skipped",
        nolist: "the vendor's model list failed: {detail}",
        nomodels: "the vendor lists no model",
        allfail: "all {n} models failed, nothing was changed: the provider is kept, fix the key or the URL and paste it again",
        capped: "only the first {count} models were tested; the other {untested} are left as they were",
        timeout: "the test ran too long; what wasn't tested is left as it was",
        nosave: "saving failed: {detail}",
        noapi: "none of magpie's APIs answered ({detail}); it is saved as an OpenAI-compatible URL and the test tries again",
      },
    },
  };
  let S = WORDS[langOf().startsWith("zh") ? "zh" : "en"];
  const fill = (s, v) => {
    for (const k in v || {}) s = s.split("{" + k + "}").join(v[k] === undefined ? "" : v[k]);
    return s;
  };
  const t = (key, v) => fill(S[key] || key, v);
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const title = $id("agentChatTitle");
  const sub = $id("agentChatSub");
  const maxLabel = $id("agentChatMaxLabel");
  const maxUnit = $id("agentChatMaxUnit");
  // the window's own labels, in the page's language: it is read again each
  // time the window is opened, since Settings can change it while it is shut
  function relabel() {
    S = WORDS[langOf().startsWith("zh") ? "zh" : "en"];
    if (title) title.textContent = S.title;
    if (sub) sub.textContent = S.sub;
    if (maxLabel) maxLabel.textContent = S.max;
    if (maxUnit) maxUnit.textContent = S.unit;
    if (sendBtn) sendBtn.textContent = S.send;
    input.placeholder = S.ph;
    openBtn.title = S.title + "：" + S.sub;
    if (maxIn) maxIn.title = S.max + " " + S.unit;
  }
  relabel();
  const scroll = () => { body.scrollTop = body.scrollHeight; };
  let busy = false;

  // a line of the run: a dot in its colour, then what it says
  function line(box, level, text, mono) {
    const r = el("div", "ac-row " + level);
    r.append(el("i", "ac-dot"));
    const cell = el("div", "ac-text");
    if (mono) cell.append(el("span", "ac-mono", mono + "  "));
    cell.append(document.createTextNode(text));
    r.append(cell);
    box.append(r);
    scroll();
    return r;
  }
  function chips(box, name, ids, cls) {
    if (!ids || !ids.length) return;
    const c = el("div", "ac-chips");
    c.append(el("span", "ac-note", name));
    for (const id of ids) c.append(el("span", "ac-chip " + cls, id));
    box.append(c);
  }
  function addMe(text) {
    const m = el("div", "ac-msg me");
    m.textContent = text;
    body.append(m);
    scroll();
  }
  function addRun() {
    const wrap = el("div", "ac-msg run");
    const head = el("div", "ac-run-head", S.run);
    const box = el("div");
    wrap.append(head, box);
    body.append(wrap);
    scroll();
    return { head, box };
  }
  const endRun = (r) => {
    if (r.head.textContent === S.run) r.head.textContent = S.done;
    r.head.classList.add("done");
  };

  // the problem's own words, with its counts and the vendor's text filled in
  const problemText = (ev) =>
    fill((S.problems && S.problems[ev.code]) || ev.code, {
      count: ev.count || 0,
      untested: ev.untested || 0,
      n: ev.count || 0,
      detail: ev.detail || "",
    });

  // one event of the run, drawn
  function draw(ev, r) {
    const box = r.box;
    switch (ev.type) {
      case "parsed": {
        const parts = [];
        if (ev.base) parts.push(S.base + " " + ev.base);
        if (ev.key) parts.push(S.key + " " + ev.key);
        if (ev.models && ev.models.length) parts.push(S.models + " " + ev.models.join(", "));
        parts.push(S.name + " " + (ev.name || ev.host || "—"));
        line(box, "info", parts.join(" · "));
        break;
      }
      case "exist":
        line(box, "info", t("exist", { name: ev.name, id: ev.id }));
        break;
      case "preset":
        line(box, "info", t("preset", { name: ev.name, id: ev.id }));
        break;
      case "creating":
        line(box, "info", S.creating);
        break;
      case "created":
        line(box, "ok", t("created", { name: ev.name, id: ev.id }) +
          (ev.speaks && ev.speaks.length ? " · " + S.speaks + " " + ev.speaks.join("/") : ""));
        break;
      case "keyadded":
        line(box, "info", t("keys_", { n: ev.added }));
        break;
      case "listed":
        line(box, "info", t("listed", { n: ev.count }));
        break;
      case "testing":
        line(box, "info", t("testing", { n: ev.count }));
        break;
      case "test": {
        const via = ev.protocol ? ev.protocol + " · " : "";
        const said = ev.ok
          ? "✓ " + via + (ev.status ? ev.status + " · " : "") + (ev.ms ? ev.ms + "ms" : "")
          : "✗ " + via + (ev.status ? ev.status + " · " : "") + (ev.error || "");
        line(box, ev.ok ? "ok" : "warn", said, ev.model);
        break;
      }
      case "result": {
        const card = el("div", "ac-card");
        card.append(el("div", "ac-note", t("result", { n: (ev.kept || []).length, m: (ev.dropped || []).length })));
        chips(card, S.keep, ev.kept, "keep");
        chips(card, S.drop, ev.dropped, "drop");
        if (ev.untested) card.append(el("div", "ac-note", t("untested", { n: ev.untested })));
        box.append(card);
        scroll();
        break;
      }
      case "saved":
        line(box, "ok", t("saved", { id: ev.id, n: ev.count }));
        break;
      case "problem":
        line(box, ev.level === "warn" ? "warn" : "err", problemText(ev));
        break;
      case "say":
        if (ev.text) line(box, ev.level === "warn" ? "warn" : "info", ev.text);
        break;
      case "mcps":
        line(box, "info", t("mcps", { n: ev.count }) + (ev.models && ev.models.length ? " · " + ev.models.join(", ") : ""));
        break;
      case "mcp": {
        const states = { added: "mcpAdd", updated: "mcpUpdate", same: "mcpSame" };
        for (const s of ev.servers || []) {
          const state = S[states[s.state]] || s.state || "";
          line(box, s.state === "added" ? "ok" : "info", [s.transport, s.target].filter(Boolean).join(" · ") + (state ? " · " + state : ""), s.name);
        }
        if (ev.servers && ev.servers.length) line(box, "ok", S.mcpDone);
        else line(box, "err", S.mcpNone);
        break;
      }
      case "done":
        endRun(r);
        break;
    }
  }

  async function send() {
    const text = input.value.trim();
    if (!text || busy) return;
    input.value = "";
    addMe(text);
    const r = addRun();
    busy = true;
    sendBtn.disabled = true;
    let maxTest = parseInt(maxIn && maxIn.value, 10);
    if (!Number.isFinite(maxTest) || maxTest < 0) maxTest = 0;
    if (maxIn) { try { localStorage.setItem("magpie.agentChatMax", String(maxTest)); } catch { /* the page keeps it only */ } }
    try {
      const res = await fetch("/api/agent/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, maxTest }),
      });
      if (!res.ok) {
        let why = res.status + " " + res.statusText;
        try { const d = await res.json(); if (d && d.error) why = d.error; } catch { /* as it came */ }
        throw new Error(why);
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
          const one = buf.slice(0, i).trim();
          buf = buf.slice(i + 1);
          if (!one) continue;
          try { draw(JSON.parse(one), r); } catch { /* a line cut short */ }
        }
      }
    } catch (e) {
      line(r.box, "err", t("http", { e: e && e.message ? e.message : String(e) }));
    } finally {
      endRun(r);
      busy = false;
      sendBtn.disabled = false;
      scroll();
    }
  }

  function show(on, focus = true) {
    panel.hidden = !on;
    openBtn.classList.toggle("on", on);
    if (on) {
      relabel();
      try { const v = localStorage.getItem("magpie.agentChatMax"); if (v && maxIn) maxIn.value = v; } catch { /* the default */ }
      if (focus) input.focus();
      scroll();
    }
  }
  openBtn.addEventListener("click", () => show(panel.hidden));
  if (closeBtn) closeBtn.addEventListener("click", () => show(false));
  // open by itself once the page has settled — every time magpie starts
  // (朱, 2026-10-16) — without taking the caret: the page under it keeps its
  // own place, and a click in the box is what puts the caret in it
  setTimeout(() => { if (panel.hidden) show(true, false); }, 600);
  panel.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { show(false); return; }
    if (e.key === "Enter" && !e.shiftKey && e.target === input) { e.preventDefault(); send(); }
  });
  sendBtn.addEventListener("click", send);
})();
