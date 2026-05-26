# Box Model · 开发 SOP

> 本文是后续每个需求的标准操作流程(SoR)。每条 R0.x/R1.x/... 必须按这 5 阶段顺序走,产出物逐阶段验证。

## 角色分工

| 角色 | 持有方 | 职责 |
|---|---|---|
| 架构师 | Claude(主对话) | 阶段 1/2/4/5 |
| 实现者 | 子 agent(`Agent` 工具) | 阶段 3 |
| 决策者 | 用户 | 拐点拍板(优先级、SoR 归属、API 形状二选一) |

## 五阶段 SOP

每个需求按这 5 阶段顺序走,前阶段产出是后阶段输入。**前阶段未完成的产出物,后阶段必须拒收**。

```
┌─[1] clarify ──┐   ┌─[2] design ───┐   ┌─[3] implement ─┐   ┌─[4] validate ─┐   ┌─[5] release ─┐
│  需求澄清     │ → │  架构设计     │ → │  TDD 实现      │ → │  亲自验收     │ → │  登记 + 债务 │
└───────────────┘   └───────────────┘   └────────────────┘   └───────────────┘   └──────────────┘
   架构师             架构师               子 agent             架构师              架构师
```

### [1] clarify — 需求澄清

**对应器官钉**:`warehouse/system_core/digital_infrastructure_organs/requirement_clarifier`

**输入**:
- 用户口述需求,或 ROADMAP 上的 R 条目,或上一阶段验收发现的债务

**产出**(必须):
- 一句话目标(business intent)
- 不变式影响清单(碰了 README 第几条)
- 验收清单(AC,可逐条验证的 checklist)
- 范围切分:若工作量 > 1 个 agent 单次能完成,拆 R0.X.1 / R0.X.2
- 排除项(不做什么),防止 agent 自由发挥

**输出位置**:写到 ROADMAP.md 的对应 R 条目下,或写到本次对话作为派单依据

**红线**:不能跨过这一阶段直接派 agent。**模糊需求 = 模糊代码**。

---

### [2] design — 架构设计

**对应器官钉**:`warehouse/saas_builder/architecture_forge`(active 元家族,7 行械钉)

工序简化版(本项目尺寸):
1. `analyze_system_boundary` — 这次改动碰哪些模块?
2. `design_database_schema` — 数据模型 / 字段 / 类型(若适用)
3. `design_api_contract` — Service 层方法签名 + 错误语义 + 不变式
4. `design_nfr_strategy` — 性能 / 原子性 / 持久化(若适用)
5. `generate_design_doc` — 写 spec(派单内容)

**产出**(必须):
- Spec(给 agent 的派单文件):
  - 接口契约(方法签名 + 行为 + 错误码)
  - TDD 顺序(红 → 绿 → 重构)
  - AC 硬指标(grep 数 / 测试数 / E2E 命令)
  - 不要做的事清单
  - 报告格式
- 不变式表(README 第几条 + 是否改写 + 改写后版本)
- 架构债登记:这次新增/解决哪些

**输出位置**:派单 spec 直接进 `Agent` 工具的 prompt;不变式改动同时进 ROADMAP

**红线**:Spec 必须**契约级精确**(字段名、签名、错误类型、边界情况)。Spec 模糊会导致 agent 自由发挥 → 我后面无法验收。

---

### [3] implement — TDD 实现

**对应器官钉**:`warehouse/system_core/digital_infrastructure_organs/code_forge`(active)

6 颗行械钉,**强制按顺序**:
1. `generate_code_skeleton` — 加类型 / 接口 / 空函数,让测试能编译
2. `generate_unit_tests` — **先写测试,跑红**(关键:贴红输出证明测试有效)
3. `implement_business_logic` — 让红变绿
4. `review_code_quality` — agent 自审(我会复审)
5. `refactor_code` — 必要时重构(本项目尺寸通常不需要)
6. `optimize_performance` — KISS,通常跳过

**产出**(子 agent 报告必须含):
- 红阶段输出片段(证明测试先红)
- 绿阶段 `go test ./... -count=1` 输出
- 关键 grep 验证(spec 里列的硬指标)
- 改/新增文件 + 行数
- 偏离 spec(应为空,有则列)

**红线**:agent 不准跨阶段(不准没写红测试就实现)。子 agent 必须贴红输出证明 TDD 走过红阶段。

---

### [4] validate — 亲自验收

**对应器官钉**:`warehouse/quality_assurance/test_forge`(active 元家族)

我亲自做(不只看 agent 报告),固定六步:
1. **`go test ./... -count=1`** 独立复跑(防 cache 假绿)
2. **`go vet ./...`** 干净
3. **关键 grep** 全部归零或达指标(spec 里硬指标)
4. **读关键代码段**:agent 写的方法,看是否真按契约实现(尤其错误路径)
5. **手工 E2E**(若适用):build + 跑 CLI / 真实数据
6. **架构师审查**:发现 spec 没写但 agent 写漏/写错的地方 → 立刻 patch

**产出**(必须):
- 通过/不通过决定
- 新发现的架构债登记(D#N)
- patch 需求(若需要 follow-up)

**红线**:不能只看 agent 自报"全过"。**Trust but verify**。

---

### [5] release — 登记 + 债务

**对应器官钉**:`warehouse/system_core/digital_infrastructure_organs/release_chronicler`(active)

我做:
1. 更新 ROADMAP.md:状态表(R 标 done,贴测试数)
2. 更新架构债清单:新增 D#N(每条标 owner / 影响 / 拟解决需求)
3. 必要时更新 docs/(架构变更同步到 architecture.md / observability.md / sop.md)
4. (待 Phase 1 完成)同步状态进 `dev-progress` Box(用 `box replace` 开新版本)

**产出**:
- ROADMAP 状态表 + 架构债表更新
- 若有 docs 改动,提交记录

**红线**:不更新 ROADMAP 等于交付未完成。**债不记账 = 债务永久丢失**。

---

## 阶段间的契约(强制)

| 上游产出 | 下游消费 | 不一致时 |
|---|---|---|
| clarify 的 AC 清单 | design 的 spec | spec 必须每条 AC 都有对应实现/测试 |
| design 的 spec | implement 的 agent prompt | spec **整段**塞进 prompt,不许 paraphrase |
| implement 的报告 | validate 的硬指标 | 报告里每个 grep / 测试数都要架构师独立复验 |
| validate 的债务 | release 的 ROADMAP 表 | D#N 必须立刻登记 |

## 何时跳阶段(罕见)

- 5 行以内的纯修复(改 typo / fmt) → 跳 1+2+3+4,只走 5(记一行 commit-级变更)
- 重构无业务逻辑 + agent 完全可机械执行 → 1 可简化为一行目标,2 必须保留
- 紧急回滚 → 5 优先(立即恢复),回头补 1-4 的产出物

## 何时该停

任何阶段出现以下情况立即停止,回上阶段重新做:
- clarify 后发现 AC 不可验证 → 重新 clarify
- design 后 spec 自相矛盾 → 重新 design
- implement 报告偏离 spec 超过 1 条 → 回去 patch(像 R0.4 的 ListBoxes 那样)
- validate 发现根因性架构污点 → 不放行,要么 follow-up patch 要么回 design

## 实例:R0.5 走完整 SOP

| 阶段 | 实际产出 |
|---|---|
| clarify | 5 条债务 D#2/D#5/D#6/D#9/D#10 + 每条根因 + AC 清单 |
| design | "code_forge a3 阶段"标识 + 4 个新方法契约 + UpdateLabelsOption pattern + history-guard 语义 |
| implement | 子 agent TDD 跑红 → 17 新测试绿 + 3 老测试重构 |
| validate | 我跑 `go test ./... -count=1` → 85 全绿 / 3 grep 硬指标全过 / D#9 E2E 真验证 |
| release | ROADMAP 表 R0.5=done / 12 债减到 7 / 本 SOP 文档落地 |

---

## 物料模板

派单 spec 的标准章节(直接复制改)。**每个 spec 顶部必须含 nail 序列声明**:

```
# 任务:R<编号> — <一句话目标>

## SOP 阶段定位
- **本次走 code_forge 器官钉**(active),序列:
  - a1 generate_code_skeleton    — <要建的类型/接口/常量>
  - a2 generate_unit_tests       — <要写的测试用例,先跑红>
  - a3 implement_business_logic  — <让红变绿>
  - a4 review_code_quality       — <agent 自审清单,我会复审>
  - a5 refactor_code             — <若需要,通常跳过>
  - a6 optimize_performance      — <KISS,通常跳过>
- 上游器官钉(本次 spec 由谁澄清):requirement_clarifier(架构师做)
- 下游器官钉(本次 spec 验收谁做):test_forge + acceptance_gateway(架构师做)
- 报告时必须**显式标注每个 nail 的产出**(贴红测试 = a2 完成证据,贴绿测试 = a3 完成证据)

## 仓库
<路径 + Go 版本 + 当前测试数>

## 架构师已定决策(不可偏离)
<契约 / 字段 / 行为 / 不变式影响>

## TDD 严格顺序(对齐 code_forge a1→a3)
### 步骤 1 (a1 + a2) — 写测试,跑红
<列测试用例,跑红后贴 1-2 条失败>
### 步骤 2 (a3) — 实现
<按契约改>
### 步骤 3 — 全绿
<go test ./... -count=1 -v>

## 验收 AC
- [ ] <grep 硬指标>
- [ ] <测试数>
- [ ] <E2E 命令 + 预期输出>
...

## 不要做的事
<排除清单>

## 报告(≤ N 字)
1. **a1 产出**:列出新增类型/接口/常量
2. **a2 红阶段**:粘 1-2 条失败输出
3. **a3 绿阶段**:`go test ./... -count=1` 输出
4. **a4 自审**:agent 自查清单(关键 grep / 边界 case)
5. 其余按格式
```

**红线**:派单时把这个模板**整段**塞进 agent prompt,不许 paraphrase nail 序列(否则等于隐式 SOP,我无法 acceptance_gateway 验"哪步漏了")。

## SOP 编号规则(器官钉锚点)

每个 R 的 spec 顶部三行强制:

```
SOP 阶段定位:
- nail 序列:code_forge(active) ▸ a1 → a2 → a3
- 上游:requirement_clarifier(架构师产出 → 即本 spec 顶部"架构师已定决策"那段)
- 下游:test_forge + release_chronicler(我做)
```

未来 R 派单**少这三行 = 直接拒收 spec、重新派**。

## 历史(本规则的触发)

| 日期 | 事件 |
|---|---|
| 2026-05-23 | R0.7.1 派单 spec 漏显式 nail 序列(隐式走 code_forge.a1→a3 但未标注)→ 用户提醒"注意使用 nail 的 sop 开发" → 增此模板节,R0.7.2 起强制 |
| 2026-05-23 | R0.7.6 派单 spec 假设 NailForge `nail.atom` 是字符串,实际是 map(`{transform_type, method, object, state_transition}`)→ agent 主动发现并加 `parseAtomMap` 桥接 + 一个新测试 → **教训**:派单前 **clarify 阶段**必须**真读一份样本 YAML/数据**,不要凭"应该长这样"假设字段形态。已在派单模板的 clarify 章节加 "若涉及外部数据格式,粘 1 份真实样本到 spec" 这条要求 |
