# task-router

按**请求内容**给每个请求选模型/路由分组的网关中间件——"模型群体智能"（Collective
Model Intelligence）里 Mio 路由思路的轻量落地版：便宜能干的模型接代码、强模型接
硬推理、中文优化的模型接中文闲聊，各归其位。

两种模式：

| 模式 | 选型依据 | 配置成本 |
|---|---|---|
| `groups`（默认） | 手工分组映射：分类 → `group/<id>` | 建好分组 + 一行映射 |
| `profiles` | **实测能力画像 × 成本**：分类 → 按该类得分过滤门槛 → 质量优先 → 便宜的优先 | 跑一次评测（24 题 × 候选模型数） |

`profiles` 模式对应论文 Model Eval + Mio 的思路：用**少量有区分度的题目**估出每个
模型在各类任务上的得分（能力画像），路由时按画像和成本选模型。

## 原理

```
serveAgent
  ├─ 中间件链 onRequest(body, ctx)   ← 本插件在这里改写 body.model
  └─ s.serve → provider.Resolve       ← 网关按改写后的 model 选池
```

`provider.Resolve` 对 `group/<id>` 形式的模型 ID 直接解析到路由分组
（`internal/provider/group.go` 的 `GroupPrefix = "group/"`）；对普通
`provider/model` ID 解析到对应供应商。所以中间件改写 `body.model` 就能选择
由谁服务。

## 安装

```sh
magpie plugin add "E:\\git\\ai\\magpie\\packages-local\\task-router"
```

本地路径插件按原位置引用，改文件即生效（重载中间件后）。

## groups 模式（方案一，零评测成本）

先在 Routing 页建好分组，然后在 options 里映射：

```json
{
  "enabled": true,
  "mode": "groups",
  "groups": { "code": "coding", "reasoning": "reasoning", "chinese": "chinese" }
}
```

## profiles 模式（方案二：实测画像驱动）

### 第 1 步：跑画像评测

```sh
cd E:\\git\\ai\\magpie\\packages-local\\task-router\\profile
node run-profile.mjs ^
  --models "deepseek/deepseek-chat,glm/glm-5.3,anthropic/claude-opus-5-5" ^
  --key sk-magpie-key-你的网关key ^
  --prices "{\"deepseek/deepseek-chat\":{\"in\":2,\"out\":8},\"anthropic/claude-opus-5-5\":{\"in\":65,\"out\":325}}"
```

- 评测走**网关本身**（默认 `http://127.0.0.1:3425`），与 Agent 同一条路径；
  需要 `--key`（网关 key，`sk-magpie-key-...` 形状）。
- 题集是 `profile/questions.json`：24 道有区分度的题（code/reasoning/chinese/
  general 各 6 道），判分器全部确定性（exact / contains / regex），见
  `profile/grade.mjs`。
- `--prices` 是可选的 CNY/百万 token 价目；给了才会算 `costPerRequestCNY`
  （按每题实测 token 数折算）。不给则只按质量排序。
- 24 题 × 模型数 次真实调用——是真实计费流量，但量很小。

### 第 2 步：推送画像进插件 options

```sh
node push-profile.mjs ../profiles.json --mode profiles --enable
```

脚本按 magpie 自己的规则找 `plugins.json`（`~/.config/magpie/`，`appdir.Config`），
把画像合并进 task-router 的 options，并先做时间戳备份。

### 第 3 步：重载

重启 magpie，或在 Plugins 页把 task-router 关了再开。

### 选型逻辑（`pickByProfiles`）

1. 请求分类 → 取该类别的得分列（无该列用 `overall`）；
2. 得分 ≥ `minScore` 的才入围；
3. **质量高者优先**；同分→**便宜者优先**；再同→`overall` 高者优先；
4. 选出的模型 ID 写入 `body.model`（形如 `deepseek/deepseek-chat`）。
5. 画像缺失/无人入围 → 自动回落到 groups 模式逻辑；再不行 → 原模型原样。

## options 总表

| 选项 | 默认 | 说明 |
|---|---|---|
| `enabled` | `false` | **总开关，默认关** |
| `mode` | `"groups"` | `"groups"` 或 `"profiles"` |
| `groups` | `{}` | 分类 → 分组 id（groups 模式用；profiles 模式作回落） |
| `defaultGroup` | `""` | groups 模式下没命中分类时的兜底分组 |
| `profiles` | 无 | `profiles.json` 的内容（push-profile 写入） |
| `minScore` | `0.5` | profiles 模式的入围分数线（0-1） |
| `overrideExplicitGroups` | `false` | Agent 已显式请求 `group/...` 时是否仍改写 |
| `lookbackMessages` | `3` | 向前看几条 user 消息做分类 |
| `cjkRatio` | `0.25` | 判定"中文请求"的 CJK 占比阈值 |

## 分类规则（内置，按序匹配，最新消息优先）

| 分类 | 触发特征 |
|---|---|
| `code` | 代码围栏、function/class/def/import、报错堆栈、npm/git 命令、源文件扩展名、bug/fix/编译 |
| `reasoning` | prove/derive/定理、step by step、概率/优化；证明/推导/求解/推理 |
| `chinese` | CJK 字符占比 ≥ `cjkRatio` |
| （无命中） | general——profiles 模式用 `scores.general`，groups 模式用 `defaultGroup` |

## 安全性

- **未启用 = 零动作**；**显式 `group/...` 受尊重**；**钩子永不破坏请求**（异常即放弃改写）。
- profiles 模式选出的模型与当前相同 → 不改写。
- 画像过期了怎么办：重新跑一遍评测、再 push 即可；或把 `mode` 切回 `groups`。

## 已知边界

- 只识别 OpenAI / Anthropic 风格的 `messages` 请求体；Gemini `contents` 风格保持原样。
- 分类是启发式规则；判分是确定性文本匹配——都是画像思路的**极简版**，不是论文的
  Model Eval 全量实现（跨域推断、题库自适应选题等未做）。
- 每请求只改写一次模型（选池），不做多步协作——那是方案三的事。
- 评测题集偏通用：想让画像贴合你的真实负载，把 `questions.json` 里的题换成你
  业务里的典型请求即可（保持 grader 结构）。
