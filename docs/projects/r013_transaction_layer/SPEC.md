# R0.13.1 · Box 程辙层 · 技术方案

> 风隐造词约定:**程辙** = transaction 替换词(程=程序/路程,辙=路径痕迹)。
> 全文用"程辙"系命名,**脱 ACID 词汇陷阱**(codex 评审 ① 建议)。
> 不动:nail_dag(NailForge 既有)/ 不变式 #10 #11(README 既有)/ MCP tool 名(R0.10 已实现)。
>
> 来源:database_engine_forge nail 链 a1-a6 跑出(2026-05-25),核心产物 a6 design_consistency_layer。
> 原始 JSON:`runs/a1-a6_run_1.json`(机器视图)/ 本文档:人类视图(ADR + mermaid + checklist)。
>
> ## 风隐造词对照
>
> | ACID 词汇                    | 风隐造词    | 释义 |
> |---|---|---|
> | 事务层 / transaction layer   | **程辙层**  | 一程之辙(执行路径) |
> | session                      | **一程**    | 一次执行的程 |
> | token                        | **程符**    | 一程的凭证 |
> | BEGIN                        | **启程**    | 开启一程 |
> | COMMIT                       | **合程**    | 程归正轨 |
> | ABORT                        | **断程**    | 中断一程 |
> | GET                          | **观程**    | 观察一程 |
> | isolation level              | **位阶**    | 可见性阶位 |
> | consistency model            | **同辙**    | 路径同纹 |
> | replication                  | **衍程**    | 程的衍生 |
> | lifecycle boundaries         | **程界**    | 程的边界 |
> | crash recovery               | **崩归**    | 崩后归元 |
> | trace                        | **程纹**    | 程的痕迹 |
> | rollback                     | (删)       | box 不做反向,故无词 |
> | saga without compensations   | **不复辙**  | 一程不可逆 |

## 1. 背景

```
为已完成的 box-model(KM dumb storage,224 测试,Phase 0+1+R0.10 done)加程辙层:
  - 程符系统(进程级 sync.Map,重启失效)
  - 写操作 opt-in 程符写 → 自动 AppendTaskTrace
  - 程界 3 边界:启程 / 合程 / 断程
  - 不变式 #10:dumb storage 保持(程符不锁 item,不复辙)
  - 不变式 #11:程符 = 一程标识,not authorization(无程符写仍成功)
```

## 2. 关键决策(ADR)

| 决策 | 选择 | 拒绝的方案 + 理由 |
|---|---|---|
| 程辙模型.纹 | **`一程`**(进程级一次执行) | `acid` 需反向 + 位锁 / `none` 描述不准(有程界)/ `base` 分布式概念 |
| 位阶 | `程符_位阶`(同程内 read-your-writes) | 7 标准级别全不可达(见 §6)|
| 位锁 | `无` — 不锁 item,不创版本 | 不变式 #10+#11 联合要求 |
| 衍程 | `无` | single_node 部署,一人公司 |
| 同辙 | `n_a` | 无多节点 |
| 反向(rollback) | **不复辙** | 断程仅 trace `✗` + reason,不撤销程符期间 box 写入 |
| 程符持久化 | **不持久化**(进程级 sync.Map) | 重启失效 = feature,不是 bug |

confidence 各项 0.85-0.95(高,因约束源自不变式 + R0.13.1 spec,推导链短)。

## 3. 事件链与觉痕游标(path-centric 模型)

> codex 二轮抓的核心:R0.13.1 不是"状态机 + 终态"模型,是 **一程 = append-only 事件链 + 觉痕游标**。
> 觉痕(✓/✗/?/→/~/◯)= 当前观察标记,可被后续覆写,**不存在"终态"**。
> 启程/合程/断程 = 都是 append 一条事(event),不是"状态切换"或"边界过渡"。
> 程辙 ≠ ACID transaction;程辙 = 一段路径上的事件流。

### 3.1 事件链可视化(取代状态机)

```mermaid
sequenceDiagram
    participant agent
    participant box
    participant trace as 程纹(jsonl)
    
    agent->>box: 启程(事)
    box->>trace: append {op:启程, 觉痕:→, 程符前缀, at}
    Note over box: 觉痕游标 →
    
    loop 一程内任意次
        agent->>box: 程符写(事 · opt-in token-gated)
        box->>trace: append {op:..., 觉痕保持或变, args_summary, at}
    end
    
    alt 合程
        agent->>box: 合程(事)
        box->>trace: append {op:合程, 觉痕:✓, summary, at}
    else 断程
        agent->>box: 断程(事)
        box->>trace: append {op:断程, 觉痕:✗, reason, at}
    else 进程崩
        Note over box: 程符隐式失效(sync.Map 清空)<br/>★ v0.1 startup hook 扫纹末行未闭合 →<br/>append {觉痕:?, reason:孤程_由_崩裂}
    end
    
    Note over agent,box: 觉痕(✓/✗/?/→/~/◯)可被 agent 后续覆写<br/>"合程"后仍可继续 append 事 + 翻觉痕<br/>不存在"终态"—— 程是 append-only event ledger
```

### 3.2 模型骨架(对照表)

| 状态机思维(已弃) | path ledger 思维(本层真模型) |
|---|---|
| 候程 / 行程 / 已合 / 已断 / 孤程 是 5 个**状态**,有过渡 | 没有"状态" — 只有 **事件链 +当前觉痕游标** |
| 启程/合程/断程 是 3 个**边界**,过渡时触发 | 启程/合程/断程都是 **append 一条事**,等同于任何其他 op |
| 合程 = 完成 = 不可再改 | 合程 = 标个 ✓ 觉痕 + 撤 token;**后续仍可 append 事 + 翻觉痕** |
| ✓/✗ 是终态 | ✓/✗/?/→/~/◯ 是当前**觉痕**,随时可被新事覆写 |
| 故障恢复 = 回到一致状态 | 崩归 = 程符失效 + 程纹保留 + startup 补一条孤程事 |
| ACID(原子提交 / 完整性约束) | "不复辙" + 续纹(只可加,不可改历史事)|

### 3.3 四种事(4 events)的细节

> **关键:这 4 个不是边界,是 4 种 event op,平等地 append 到程纹**。下面只描述每种 event 触发时 box 做什么。

#### 启程 — `box_task_start({task_id?, source, goal, pass_criteria, nail_chain, caller_id})`

### 启程 — `box_task_start({task_id?, source, goal, pass_criteria, nail_chain, caller_id})`

```
1. 无 task_id → CreateTask 生成 Item(kind=task)
   有 task_id → 校验存在(允许对已有 task append 启程事)
2. 程符 = "tsk_" + base64(crypto/rand 16 bytes)
3. sync.Map.Store(程符, 一程{
       task_id, caller_id, created_at: time.Now().UTC()
   })
4. AppendTaskTrace(task_id, TraceStep{
       op: "task_start",
       token_prefix: 程符[:12] + "...",   ★ 不写全程符(防日志泄漏)
       caller_id,
       at: now,
   })
5. SetItemSymbols(task_id, [{Kind:Status, Value:"→"}])    ← 翻觉痕,可后续被任何事再翻
6. return {task_id, 程符}
```

注意:
- 同 task_id 允许多次启程(每次新发程符,append 一条启程事)
- 同 task_id 允许多程符并存(并发场景,last-writer-wins on 程纹);v0.2 可加 `reject_if_active_程符` 选项
- **启程后 task 也允许直接被覆写到 ✓(无须合程)— 觉痕是游标,启程只是 append 一事**

### 合程 — `box_task_finish({token, status:"✓", summary?})`

```
1. 一程, ok := sync.Map.LoadAndDelete(程符)   ★ 原子 lookup + revoke
2. !ok → return Err未知程符
3. AppendTaskTrace(一程.task_id, TraceStep{
       op: "task_finish", status: "✓", summary, caller_id: 一程.caller_id, at: now
   })
4. SetItemSymbols(一程.task_id, [{Kind:Status, Value:"✓"}])
5. return ok
```

**`合程` 不是"终态" — 是 append 一条 ✓ 觉痕事**:
- 不真原子提交多 op(box=dumb storage,各写操作落地时已可见)
- 合程后程符撤销,**但 task 自身仍可被覆写**(再启程发新程符 / 直接 set_status 翻 →)
- 觉痕(✓)只是当前观察标记,后续任何事都可翻它
- 跟 SQL `COMMIT` 的最大差别:**SQL commit 后改不了,合程后还能继续 append 事**

### 断程 — `box_task_abort({token, reason})`

```
1. 一程, ok := sync.Map.LoadAndDelete(程符)
2. !ok → return Err未知程符
   ★ idempotent:重试两次第二次为 noop
3. AppendTaskTrace(一程.task_id, TraceStep{
       op: "task_abort", status: "✗", reason, caller_id, at: now
   })
4. SetItemSymbols(一程.task_id, [{Kind:Status, Value:"✗"}])
5. return ok
```

**断程同理 — append 一条 ✗ 觉痕事,不是"终态"**:
- 不撤销程符期间写入 box 的任何 Item/Symbol/Labels 修改(不变式 #10 + #11)
- 觉痕(✗)可后续被任何事翻回 → 或 ✓(agent 决定 task 是否要 reactivate)
- 补偿由调用方自行写(**不复辙** — 一程不可逆,但 task 可重启)
- 跟 SQL `ROLLBACK` 的最大差别:**SQL rollback 撤销所有写,断程一字不撤,只 append 标记**

### 观程 — `box_task_get({task_id} OR {token})`

read-only:返 task 当前**觉痕游标**(symbol) + 程纹尾行(最近一事) + (若传程符) 程符是否仍 active。

**观程不 append 事** — 纯读路径。

### 关键不变式(path ledger 三句话)

```
1. 一程 = append-only 事件链 + 觉痕游标
2. 启程/合程/断程/任何写 都是 append 一事,平等地
3. 觉痕(✓/✗/?/→/~/◯)是游标,可被任意后续事覆写;不存在"终态"
```

### 程符写(opt-in,M1 interior 6 tool)

```
box_store / box_replace / box_tag / box_delete / box_consume / box_set_item_symbols
   带程符  → service 自动 AppendTaskTrace({op, token_prefix, args_summary, at})
   不带程符 → 走旧路径(保 224 老测试 / 不变式 #11)
```

## 4. 崩归(crash recovery)

```
process restart
  → in-memory sync.Map 清空
  → 所有在程的程符自动失效
  → in-flight tasks 隐式 auto-abort(程符不可用,但 ★ 不向程纹写断程 event → 孤程)
  → box FileStore 既有 Item/Symbol/Trace.jsonl 完整保留(journal-replay durability)
  → 重启后 caller 查程纹末行:
       末行 != task_finish/task_abort → '孤程'(caller-side decision)

衍程崩裂: n_a — single node 无 split brain
  (即便重启窗口期有重叠,旧进程程符在新进程 sync.Map 查不到自然失效)
```

**已知观测断口**:孤程不会自动留 `✗` 程纹标记。

**v0.1 补**(codex 评审 ③ 建议):startup hook 扫程纹末行未闭合的 task,append `{status:"?", reason:"孤程_由_崩裂", at:startup_ts}`。**不留 v0.2**(观测断口不可拖)。

## 5. 数据形态(含 R0.10 v2 schema 升级)

### 5.1 一程(YiCheng)— 本层新增

```go
// YiCheng - 一程 · 内存 only · 进程级
type YiCheng struct {
    TaskID    string
    CallerID  string
    CreatedAt time.Time
}

// 模块级 sync.Map: 程符 (string) → *YiCheng
var yiChengSessions sync.Map
```

字面对照(代码英文 ↔ 文档中文):

| Code (Go) | 文档(中文)|
|---|---|
| `YiCheng`        | 一程 |
| `yiChengSessions`| 程符表 |
| `tokenPrefix`    | 程符前缀 |
| `StartYiCheng()` | 启程 |
| `FinishYiCheng()`| 合程 |
| `AbortYiCheng()` | 断程 |
| `GetYiCheng()`   | 观程 |

### 5.2 CreateTaskRequest(R0.10 v2 升级)

```go
type CreateTaskRequest struct {
    Intent       string
    Source       []Symbol
    Goal         []Symbol
    PassCriteria PassCriteria       // ★ v2: 支持复合
    NailDag      []NailDagNode      // ★ v2: 替代 v1 的 NailChain[]string
}
```

### 5.3 NailDagNode(R0.10 v2 新增,替代线性 NailChain)

```go
type NailDagNode struct {
    ID         string   `json:"id"`                    // 节点唯一 id (如 "a3")
    NailRef    string   `json:"nail_ref"`              // (如 "database_engine_forge/a3")
    DependsOn  []string `json:"depends_on,omitempty"`  // 上游 id 列表;空 = root
    BranchID   string   `json:"branch_id,omitempty"`   // 同分支共享,trace 渲染分组用
}
```

R0.13.1 自身的 nail_dag 形态(本项目执行链):

```yaml
nail_dag:
  - {id: a1, nail_ref: database_engine_forge/a1, depends_on: [],          branch_id: main}
  - {id: a2, nail_ref: database_engine_forge/a2, depends_on: [a1],        branch_id: main}
  - {id: a3, nail_ref: database_engine_forge/a3, depends_on: [a2],        branch_id: design_data}
  - {id: a4, nail_ref: database_engine_forge/a4, depends_on: [a2],        branch_id: design_storage}
  - {id: a5, nail_ref: database_engine_forge/a5, depends_on: [a2],        branch_id: design_query}
  - {id: a6, nail_ref: database_engine_forge/a6, depends_on: [a2],        branch_id: design_consistency}
  - {id: a7, nail_ref: database_engine_forge/a7, depends_on: [a3,a4,a5,a6], branch_id: main}  # 汇合
  - {id: a8, nail_ref: database_engine_forge/a8, depends_on: [a7],        branch_id: main}
  - {id: a9, nail_ref: database_engine_forge/a9, depends_on: [a8],        branch_id: main}
```

box 仅 schema 校验:**节点 id 唯一 / depends_on 引用必存在 / 无环**(关系完整性,不算业务智能)。**DAG 执行(topological sort + 并发分支 + 汇合)** 完全归 agent。

### 5.4 PassCriteria(R0.10 v2 升级 — 支持复合)

```go
type PassCriteria struct {
    // single 模式(kind ∈ exists/absent/all_match/count_eq)
    Kind   string      `json:"kind"`
    Query  SymbolQuery `json:"query,omitempty"`
    Arg    int         `json:"arg,omitempty"`
    Reason string      `json:"reason"`
    
    // compound 模式(kind = "compound")
    Operator    string         `json:"operator,omitempty"`     // "AND" | "OR"
    SubCriteria []PassCriteria `json:"sub_criteria,omitempty"` // ≥ 2,嵌套
}
```

R0.13.1 自身的 pass_criteria 示例(3 条 AND):

```yaml
pass_criteria:
  kind: compound
  operator: AND
  reason: "R0.13.1 落地 = 实现 + 测试零回归 + 不变式入 README"
  sub_criteria:
    - kind: exists
      query: {labels: {req_id: "R0.13.1"}, value_filter: ["✓"]}
      reason: "R0.13.1 item status ✓"
    - kind: exists
      query: {labels: {"__sem:topic": "invariant_11"}}
      reason: "不变式 #11 笔记入 box"
    - kind: count_eq
      query: {labels: {"__op:regression": "test_baseline"}}
      arg: 0
      reason: "0 个老测试 regression"
```

box 仅 schema 校验:**kind=compound 时 operator 必填 + sub_criteria ≥ 2 + 嵌套每条仍是合法 PassCriteria(递归校验)+ 嵌套深度 ≤ 3**(防 DoS)。**评估 AND/OR / 跑 sub.query** 归 agent。

### 5.5 TraceStep(R0.10 v2 升级 — 加 DAG 标记)

```go
type TraceStep struct {
    Op          string         `json:"op"`
    Status      string         `json:"status,omitempty"`     // ?/→/✓/✗/~/◯
    NailRef     string         `json:"nail_ref,omitempty"`
    NodeID      string         `json:"node_id,omitempty"`    // ★ v2: 对应 nail_dag.id (如 "a3")
    BranchID    string         `json:"branch_id,omitempty"`  // ★ v2: 分支聚合用
    Args        map[string]any `json:"args,omitempty"`       // 含 token_prefix / args_summary
    Result      map[string]any `json:"result,omitempty"`
    Reason      string         `json:"reason,omitempty"`     // abort 用
    Summary     string         `json:"summary,omitempty"`    // finish 用
    CallerID    string         `json:"caller_id,omitempty"`
    Step        int            `json:"step"`                 // ★ D#16 去 omitempty,step=0 也显式
    At          time.Time      `json:"at"`
}
```

trace.jsonl 仍线性追加(并发分支 append 可能乱序),agent 用 `node_id + branch_id` 重组 DAG 视图。box 不做 reorder。

## 6. 位阶外(诚实 — 这些标准 isolation 级别本层不可达)

| Isolation Level | 为什么本层不可达 |
|---|---|
| read_uncommitted | 本层无合程概念,不映射 |
| read_committed | 无合程边;写即可见(比 RC 强:read-your-writes,但比 RC 弱:无 snapshot) |
| repeatable_read | 无 snapshot,他方写入立即可见 |
| serializable | 无位锁 / SSI 机制 |
| snapshot_isolation | 无 MVCC |
| causal_consistency | 无 vector clock / version tracking |
| linearizability | sync.Map 自身 linearizable;但跨 sync.Map + Item store 复合操作不 linearizable;显式不主张 |

## 7. omissions v0.1(诚实留 6 条 — 注意:crash 补标已 v0.1 入)

```
1. ✗ 复辙(rollback of storage writes)— 断程仅 ✗ + reason,不反向
2. ✗ 跨进程程符 — 不持久化
3. ✗ 严格 ACID — 仅一程内 read-your-writes
4. ✗ 程符 idle TTL / expiry(仅进程生命期)→ v0.2 可加
5. ✗ 并发程符互斥(同 task_id 允许多程符并存;last-writer-wins on 程纹 order)→ v0.2 可加 `reject_if_active_程符`
6. ✗ 程符 revocation API(除合程/断程外无 admin 撤销)

★ 原 v0.1 omission "crash-orphan auto-mark" 已 v0.1 入(§4 崩归 startup hook · codex 评审 ③ 建议)
```

## 8. 不变式遵守

| 不变式 | 本层如何遵守 |
|---|---|
| #10 dumb storage | 不引 storage-side 复辙,不位锁 item;断程仅标 ✗,不反向 |
| #11 程符 = 一程标识 | 无程符写仍成功(保 224 测试);程符不是鉴权 |
| #11 corollary | 程符不持久化,restart wipes all |
| tool 命名稳定 | `box_task_start/finish/abort/get` 不破 R0.10 M1 接口(Go 内部 = Yi Cheng,MCP 对外仍叫 task)|
| R3.2.1 观测 | 4 个新 verb 仍走 obs.Inc 模板(attempt/success/error)|

## 9. 实现验收 AC(给开发 agent)

### 9.1 程辙层(R0.13.1 主体)

- [ ] 224 老测试零回归(无程符路径不变)
- [ ] +15-20 新测试覆盖:启程/合程/断程/观程 + 程符写自动程纹 + 崩归模拟 + idempotent retry + ★ 孤程 startup hook
- [ ] `Service.StartYiCheng / FinishYiCheng / AbortYiCheng / ValidateYiCheng` 4 新方法
- [ ] `WithYiChengToken(程符)` Option 加进所有 6 写方法(opt-in)
- [ ] `cmd/box-mcp/main.go` 新增 4 MCP tools(start/finish/abort/get,程符-aware)
- [ ] `README.md` 第 11 条不变式(程符 = 一程标识,not authorization)
- [ ] `box.YiCheng` struct + 模块级 `yiChengSessions sync.Map`
- [ ] 程符形态 `"tsk_" + base64(crypto/rand 16 bytes)`
- [ ] 程纹中只存 `程符前缀`(前 12 字符 + "...")不存全程符
- [ ] ★ 启动 hook:`OpenFileStore` 加 startup orphan-scan,扫各 task trace.jsonl 末行未闭合 → append `{status:"?", reason:"孤程_由_崩裂", at:startup_ts}`(codex 评审 ③ 建议)

### 9.2 R0.10 v2 schema 升级(本次合并做)

- [ ] `NailDagNode` 类型加进 model.go(替代 `[]string nail_chain`)
- [ ] `PassCriteria` 支持复合:kind="compound" + Operator + SubCriteria 嵌套
- [ ] `TraceStep` 加 `NodeID` + `BranchID` 字段
- [ ] `TraceStep.Step` 去 omitempty(D#16 顺手清)
- [ ] **box schema 校验** :
  - [ ] nail_dag 节点 id 唯一
  - [ ] depends_on 引用必存在节点 id
  - [ ] 无环检测(reject if cycle detected)
  - [ ] pass_criteria.kind=compound 时 operator ∈ {AND, OR} + sub_criteria ≥ 2
  - [ ] pass_criteria 嵌套深度 ≤ 3(防 DoS)
- [ ] **box 不做**(防止越界做"业务智能"):
  - [ ] ✗ topological sort
  - [ ] ✗ DAG 节点并发调度
  - [ ] ✗ 评估 pass_criteria.sub_criteria.query
  - [ ] ✗ AND/OR 计算
- [ ] 兼容性:R0.10 v1 老 schema(`nail_chain []string` + 简单 `pass_criteria`)继续被接受(向后兼容,转 v2 时填默认值)

## 10. 跟 R0.10 v1 → v2 的关系

```
R0.10 v1 (已 done · 2026-05-25):
  CreateTask / SetItemSymbols / AppendTaskTrace / ListTaskTrace 4 Service 方法
  Task content schema:
    intent / source / goal / pass_criteria(单条件)/ nail_chain([]string 线性)
  Trace 走 tasks/<id>.trace.jsonl 外部 jsonl
  status: ? → ✓ ✗(agent 手动调 SetItemSymbols 翻)

R0.10 v2 (本 spec 一并升级 · schema 扩展):
  ★ nail_chain([]string) → nail_dag([]NailDagNode) — 支持 DAG 分支汇合
  ★ pass_criteria 加 kind=compound + operator(AND/OR)+ sub_criteria 嵌套
  ★ TraceStep 加 node_id + branch_id(DAG 分支聚合用)
  ★ TraceStep.Step 去 omitempty(D#16 顺手清)
  ★ box schema 校验加 5 条(节点唯一/引用合法/无环/复合校验/嵌套深度)
  ★ R0.10 v1 schema 向后兼容(v1 老 task 继续读)

R0.13.1 (本 spec 程辙层,要做):
  + 程符系统(启程自动翻 ?→→ / 合程自动翻 →→✓ / 断程自动翻 →→✗)
  + 程符写自动 AppendTaskTrace(取代 R0.10 v1 的 "agent 手动调 AppendTaskTrace")
  + 程界 3 边界 MCP tools(start/finish/abort/get)
  + ★ 崩归 startup orphan-scan(codex 评审 ③ · v0.1 入)
  + 不变式 #11(README · 程符 = 一程标识 not authorization)
```

**两件事一次性合并实现**(都改 box/model.go + box/service.go,合并避免冲突):
- R0.10 v2 = schema 扩展(影响所有项目)
- R0.13.1 = 程辙层(本项目特定)

R0.13.1 在 v2 上自然受益:**task 创建时可一次声明完整 DAG + 复合 pass_criteria + 起始程符**,执行可观测、可分支、可复合判完程。

---

## 附:nail 跑链路证据

```
runs/a1_run_1.json — accept_engine_intent · 4018B · 0 escalations(ctx 清晰)
runs/a2_run_1.json — scan_engine_state_of_art · 8320B · 6 comparable engines
runs/a3_run_1.json — design_data_model_layer · 4395B · 退化诚实(degeneration_note)
runs/a4_run_1.json — design_storage_layer · 4262B · 退化 + trace.jsonl 加 event
runs/a5_run_1.json — design_query_engine_layer · 4839B · 退化 api_only
runs/a6_run_1.json — design_consistency_layer · 9904B · ★ 核心方案源
```

a7 audit / a8 bundle / a9 contract **未跑**(留 user check 后决定)。
