# What this fork adds

This is an **unofficial fork** of [yetone/magpie](https://github.com/yetone/magpie) (MIT).
Everything the upstream README describes is the app itself; this file describes the four
additions, why they are here, and how to build this build.

Upstream's policy is that it **doesn't take pull requests** ("describe it on Discord and
we'll build it"), so this lives as a fork rather than as a patch to their `main`.

## 1 · An AI assistant window

A bubble button in the header opens a small window (it opens by itself when magpie
starts; close it with × or Escape). Paste into it and it does the work:

- **A model name, a base URL and a key** — the provider is added (from a preset when the
  URL is one of theirs, else by asking which of magpie's APIs the URL answers on), the
  key is filled in, the vendor's model list is fetched, **every model is tested**, and the
  ones that answer are kept: a model that lists a name but can't be called is taken out of
  the picks, and out of the list. A URL that is already a provider is reused, not added
  twice; a paste of many keys keeps them all. `Test at most N models` (0 = all) bounds a
  long list; what isn't tested is left as it was, and when nothing answers nothing is
  changed at all.
- **An MCP config** — the `mcpServers` block every agent's file holds (or `mcp-servers`,
  `mcp`, `servers`), or one server's own object with a `command` or a `url`:

  ```json
  { "mcpServers": { "codebase-memory-mcp": { "command": "E:\\tools\\cbm.exe" } } }
  ```

  Each server is added to **Library › MCP** (`command`/`args`/`env`, or `url` with
  `headers`, `http`/`sse` chosen as the entry says). One the library already has as the
  paste has it is left alone; one whose settings changed is written over. **Nothing is
  given to an agent** — the Library's own tick row does that, so a paste can't change what
  an agent starts.

The paste is read as a provider's or as an MCP config's; the window is not a chat with a
model — it is deterministic, so nothing leaves the machine but the vendor requests the
paste itself asks for.

## 2 · Remove from the list (a model's right-click)

Right-click a model in a provider's editor → **Remove from the list**: the model is turned
off everywhere (Settings › Models › Disabled models) and leaves the list, at once. For a
vendor like NVIDIA NIM, whose list holds a hundred models a handful of which answer.

Servers' own lists are narrowed the same way for the models inside them, so a model the
user turned off no longer appears in the editor's list either (it did before: only the
agents' lists left it out).

Bring one back in **Settings › Models › Disabled models → Enable**.

## 3 · Invert selection

Next to Select all / Select none: the models the filter shows that are not picked become
picked, and those that are become not. What the filter hides, and ids typed by hand, are
left as they are.

## 4 · Remove picked

Beside the selection buttons: turns **every picked model** off everywhere in one go, after
a confirmation that says how many and what it means. It keeps what the provider served:
an invert that moved the picks onto the models being removed leaves the saved picks
standing, so a provider comes out of a cleanup serving the handful that work rather than
its whole list.

## Building this fork

Same as upstream, plus the icon and manifest resources. On Windows:

```sh
go run github.com/tc-hib/go-winres@v0.3.3 make --in build/windows/winres.json --arch amd64 --out rsrc \
  --file-version 0.1.1286 --product-version 0.1.1286
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags production -trimpath \
  -ldflags="-s -w -X main.version=0.1.1286 -H windowsgui" -o magpie.exe .
```

On macOS and Linux the upstream `Makefile` (`make app`) does it.

No Go toolchain? A current prebuilt Windows x64 exe sits under
[Releases](https://github.com/ok0735/magpie/releases/tag/v0.1.1286-fork).

> **Build it with a version above the newest official release.** magpie updates itself:
> a build whose version is older than the released one gets offered the official binary,
> and taking that update replaces the fork's build with upstream's. `0.1.1286` here is
> above the latest release at the time of writing (v0.1.1285); raise it as releases come.
> The branch was re-based onto v0.1.1285's tree (d783df46, 10-18) with every test of these
> additions passing on it.

## What is tested

- `go test ./internal/gui -run 'TestAgentChat|TestRemoveModelFromTheList|TestDisabledModelsSetting'`
  — the paste readers (a model, a URL and a key; labelled lines; an env block; a JSON
  config; a curl example; several keys and models; an MCP config's servers, added,
  repeated, written over, several at once, one that can't be a server, and a provider
  paste that must not be read as one), the provider a paste adds and what its test keeps,
  and the disabled-model list with the editor's list following it.
- `node --check` on the three scripts; the upstream `i18n-keys.test.cjs` suite.
- By hand, end to end, on Windows: the window adding a provider against a fake vendor
  (three models, one that answers 404) and keeping only the ones that answer; an MCP
  paste landing in Library › MCP; the right-click removal, invert and bulk removal; the
  icon and version resources of the built exe.
- Not covered: the upstream Playwright UI suite (no browser was available where this was
  written), and the window's own words are Chinese and English only (the app's own pages
  keep their four languages).

## License and attribution

MIT, as upstream, with their `LICENSE`, icons, name and README kept. Not affiliated with
the upstream project; the additions are mine (ok0999).

---

## 中文摘要

这是 magpie（yetone/magpie，MIT）的**非官方分支**，比原版多四件事：

1. **AI 助手窗**（顶栏气泡按钮，启动时自动弹出）：粘贴"模型名+网址+key"就自动建供应商、拉取模型、逐个测试，只保留能用的；粘贴 MCP 配置（`mcpServers`）就自动加进 **资源库 › MCP**（不会动任何 agent，勾选交给 agent 是资源库自己的事）。
2. **右键「从列表中移除」**：把某个模型全局停用并立刻从列表消失（设置 › 模型 › 已停用的模型 里可恢复）。
3. **「反选」**：全选 / 全不选 旁边新增。
4. **「移除选中」**：把当前选中的模型一次性全局停用（有确认框），并保留原本在用的那些。

编译方式与上游相同，Windows 上还要用 `go-winres` 生成图标/清单资源（见上）。**务必把版本号编得比官方最新发行版高**，否则 magpie 的自动更新会提示更新、并把分支版覆盖成官方版。当前分支已 rebase 到上游 v0.1.1285（2026-10-18，d783df46）并全部测试通过，构建版本用 0.1.1286。

> AI生成