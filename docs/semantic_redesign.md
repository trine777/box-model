# Box 语义重设计 · 风隐造词总表

> 抛弃数据库范畴,从语义重新设计 box 的概念体系。
>
> 触发:codex 二轮 review(2026-05-25)指出 "transaction → 程辙" 只换壳没换骨;
> "数据库" 这词本身携带 SQL/ACID/state-centric 范式,需整体重造。
>
> 本文 = 风隐造词总表 + 名称解释表 + 哲学差异 + 文档使用约定。
> SoR:本文(不一致时以本文为准);SPEC.md 等引用本文造词。

## 1. 范畴宣言

```
box = 符径(Symbol Path)
       一类自成的"信息存储 + 路径记录 + 符号路由"复合范畴

完整名: 符径账(Symbol-Routed Path Ledger)
英文 ID: Box  (不变,跨语言流通用)

不是: 数据库 / KV / 图数据库 / event store / 文档库 / 工作流引擎
是:   一类新东西 — 索引 + 路径 + 符号 + 账本(append-only) 的复合
```

## 2. 6 层造词总览

```
Layer 0  范畴       数据库 / database         → 符径
Layer 1  数据结构    row / column / schema     → 粒 / 维 / 形
Layer 2  索引/路径   query / index / DAG       → 寻 / 径 / 节·线
Layer 3  版本       revision / snapshot / WAL → 代 / 影粒 / 预纹
Layer 4  事件/账     event / log / replay      → 事 / 纹·账 / 复纹
Layer 5  程辙       transaction / token       → 程辙 / 程符 + 启合断
Layer 6  接口       API / CLI / MCP tool      → 接径 / 手径 / 径口
```

---

## 3. 名称解释表(★ 核心 — 每词的字源 / 隐喻 / 差别)

### Layer 0 · 范畴

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **符径** | 符=Symbol·路标 / 径=Path·路 | 符号铺成的路 | database / KM storage / KV+graph 之上 | DB 是 state 容器,符径是 path 账本(过程 vs 结果) | box 总范畴 |
| **径库** | 径=路径 / 库=storehouse | 路径的库 | storage layer | storage 中性,径库强调"存的是路径" | 当强调"存储面"时用 |

### Layer 1 · 数据结构

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **粒** | 颗粒 | 一颗信息原子 | row / record | row 是行有固定 schema,粒可任意形 + 自描述 | box.Item 单元 |
| **维** | 维度 | 可观察的轴 | column / field | column 固定列,维是动态多维标签(Symbol kind × value)| Label / Symbol 维度 |
| **形** | 形态 | 粒的形状 | schema | schema 是 strict 约束,形是当前形态(可演化) | item 当前结构描述 |
| **符录** | 符=符号 / 录=记录 | 符号化的记录 | key-value pair | KV 的 value 是 opaque blob,符录的 value 有符号语义(Symbol[] 一等公民)| item 整体形态 |
| **粒胞** | 粒+胞=细胞 | 粒的内核 | content blob | content 中性,粒胞强调"包在外层符号系统里" | Item.Content 字段 |

### Layer 2 · 索引 / 路径

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **径** | 路径 | 索引即路径 | index | 索引是查询辅助,径既是索引也是路径(走得通) | Symbol routing |
| **寻** | 寻找 | 沿径走 | query | query 是问答,寻是沿径走过去(过程感) | box_trace / box_get |
| **寻径** | 寻找+路径 | 完整动作 | query execution | 同上,带过程 | 完整查询语义 |
| **径中** | 中=命中 | 命中路径 | index hit | hit 是布尔,径中含"路径走通了"语义 | trace 结果命中 |
| **走径** | 走+径 | 遍历路径 | traversal | 同上 | DAG / neighbors 遍历 |
| **节** | 节点 | 路径上一点 | DAG node | 同(节比 node 更简洁) | nail_dag.id |
| **线** | 边 | 节间连线 | DAG edge | 简洁 | nail_dag.depends_on |
| **跃** | 跃迁 | 跨节 | hop | hop 中性,跃含"跨"的张力(类比量子跃迁) | neighbors --hops |

### Layer 3 · 版本

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **代** | 世代 | item 的代际 | revision | revision 是数字,代是"代际关系"(更亲)| Item.Revision |
| **续代** | 续+代 | 代代相承 | supersedes | supersedes 是覆盖,续代是接续(R0.13 续了 R0.11 的代) | SymRelation `>` |
| **影粒** | 影=镜像 / 粒=item | 粒的影 | snapshot | snapshot 中性,影粒强调"是 item 的影像不是 item 本身" | 备份产物 |
| **预纹** | 预=先 / 纹=痕 | 写前先纹 | WAL (Write-Ahead Log) | WAL 是日志,预纹是"事前刻痕,事后才落地" | journal-replay |

### Layer 4 · 事件 / 账

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **事** | 单一事件 | 一桩事 | event | 同(单字简洁) | 单条 trace 步骤 |
| **纹** | 痕纹 | 事的痕迹 | log / trace | log 中性,纹是不可改写的痕(自然隐喻)| trace.jsonl |
| **账** | 账本 | 纹聚成账 | ledger | ledger 是金融词,账本广义同义,中文更自然 | 总体事件账 |
| **续纹** | 续+纹 | 只可续 | append-only | append-only 强调"可加不可改",续纹同意但更短 | trace 写入约束 |
| **复纹** | 复=重 / 纹 | 沿纹再走 | replay | replay 中性,复纹强调"重走原路" | journal replay |
| **程纹** | 程=一程 / 纹 | 一程的纹 | trace of a session | 同(强调跟程辙绑定) | 每个 task 的 trace.jsonl |
| **孤程** | 孤=无主 / 程 | 无主的程 | orphaned session | 同(孤更具象)| 崩归后未闭合的程 |

### Layer 5 · 程辙(已用)

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **程辙** | 程=程序+路程 / 辙=车辙 | 走过的路 + 留下的辙 | transaction | transaction 强调原子提交,程辙强调路径痕迹(过程 vs 结果) | R0.13.1 总层名 |
| **一程** | 单次的程 | 一次执行 | session | session 是连接,一程是一次走完(自带边界感) | TokenSession Go struct = YiCheng |
| **程符** | 程+符=符节 | 一程的凭证 | token | token 是无意义串,程符暗示"持符可入此程" | 进程内 session handle |
| **启程** | 启+程 | 开始一程 | BEGIN | BEGIN 强调原子起点,启程强调路径出发 | box_task_start |
| **合程** | 合=合于轨 / 程 | 程归正轨 | COMMIT | COMMIT 是原子写库,合程是"路径走到了"(标 ✓ 不写库)| box_task_finish |
| **断程** | 断+程 | 中断的程 | ABORT | ABORT 强调撤销,断程仅"标断不撤"(box=dumb) | box_task_abort |
| **观程** | 观察+程 | 看一程 | GET | GET 中性,观程含"只看不动" | box_task_get |
| **不复辙** | 不+复=重+辙 | 不重走车辙 | "no rollback" / "saga w/o compensations" | rollback 隐含"可以回",不复辙明示"不能回"(古义"重蹈覆辙"的反面) | abort 后不撤写 |
| **位阶** | 位=位置 / 阶=层级 | 可见性阶位 | isolation level | isolation 是隔绝,位阶是"分阶可见"(更细) | visibility scope |
| **同辙** | 同=相同 / 辙 | 路径同纹 | consistency | consistency 强调状态一致,同辙强调"走的路径自洽" | consistency model |
| **衍程** | 衍=衍生 | 程的衍生 | replication | replication 是复制,衍程暗示"从主程衍生出去" | 复制策略 |
| **崩归** | 崩=崩裂 / 归=归元 | 崩后归元 | crash recovery | recovery 是恢复(回到 X),崩归是"崩后从头归元"(可继续不回滚) | process restart |

### Layer 6 · 接口

| 造词 | 字源 | 隐喻 | 对应英文 | 跟旧词本质差 | 适用边界 |
|---|---|---|---|---|---|
| **接径** | 接+径 | 径的接入点 | API | API 中性,接径明示"接到路径" | Service 公开方法 |
| **径口** | 径+口=开口 | 路径开口 | MCP tool / endpoint | endpoint 中性,径口是"路径的入口" | MCP tool |
| **手径** | 手动+径 | 手走的径 | CLI | CLI 中性,手径明示"人手介入" | box CLI |

---

## 4. 哲学差异(state-centric vs path-centric)

```
                  数据库(state-centric)        符径(path-centric)
───────────────────────────────────────────────────────────────────
状态(state)      "现在是什么"                  "从哪来 + 到哪去"
变更                atomic transition             event append
完整性              ACID 约束                     纹的不可改写
索引                查询辅助                      路径本身的一部分
故障恢复            回到一致状态                  续纹继续(残纹保留为孤程)
真值                数据库就是真值                 真值在源系统(README #1),符径是索引层
观察               读 = state snapshot           读 = 沿纹回放
```

一句话:**state-centric 关心"是",path-centric 关心"经"**。

## 5. 真值归属(不变 — README 第 1 条)

```
Source systems remain the source of truth.
Box (符径) 是索引层 + 路径账,不拥有真值。
```

符径不与数据库竞争真值地位 — **符径在数据库之上,跨数据库做索引**(README 不变式 #5)。

## 6. 文档 / 代码使用约定

### 6.1 文档(中文)

- README / SPEC.md / *.md:**全用风隐造词**(本文表为准)
- 不再用 "数据库 / transaction / commit / rollback / index" 等(除非在"对应英文"列引用)
- 旧文档(R0.x 历史)允许保留 ACID 词汇,新写作严格新词

### 6.2 代码(Go)

- 内部 struct 用**汉语拼音**(如 `YiCheng` = 一程)— LLM 友好,跨语言可读
- 公开 API 函数名用**英文**(如 `Service.StartYiCheng`)— mixin 风格,既英文又跨语言
- 但 **MCP tool 名保持英文 stable**(`box_task_start/finish/abort/get`)— 不破已发布 API

### 6.3 对话 / commit message / 注释

- 自由(架构师/agent 自由选词)
- 但 commit message 优先风隐造词

## 7. 已立的不变式(本文新增第 12 条)

```
不变式 #12 · 范畴自洽:
   Box 是"符径(Symbol Path)",不是数据库 / KV / 图 DB / event store。
   语义文档使用风隐造词体系(见 docs/semantic_redesign.md)。
   Go struct 用拼音(YiCheng / 等),MCP tool 名保持英文 stable。
   不混用 ACID / DB 词汇于新文档(SQL/transaction/commit/rollback/...
   只在"对照英文"列出现,不在主述中使用)。
```

(README 加这条不变式 — 跟既有 #10 dumb storage / #11 token=session state 互补,#12 锁住语义层。)

## 8. 演进 / 反馈循环

```
新造词反馈来源:
  - 真跑发现某词不准 → 改本文表
  - codex / 外部 reviewer 抛 → 评估纳入
  - 子 agent role-play 时困惑某词 → 改

规则:
  - 改造词 = 改本文(此为 SoR)
  - 改本文 = 通知所有文档(SPEC.md / README / 派单 spec / 等)
  - 不能局部新造词(必须先入本文)
```

## 9. 待消化的 codex 二轮抓的结构问题

```
codex 抓:"启-行-终"线性范式仍是 tx 思维
建议:append-only path ledger 模型,✓/✗/? 是观察标记不是终局

风隐版重新表达:
   程辙 不是 "启→行→终" 三态进度条
   程辙 是 "一程上的事件流" — 启程/合程/断程都只是"事"(event)
   ✓/✗/? 都是 "觉"(观察标记)— 可来回变 — 不是终局
   
SPEC.md §3 应据此重写(待 user 拍板做不做)
```

## 10. 简单符号 → 风隐映射(一眼可查)

```
Item               → 粒              SymKind        → 维种
SymRelation `<`    → 续(代际续接)    transaction    → 程辙
SymRelation `&`    → 依(依节)        token          → 程符
SymStatus `?/→/✓`  → 觉(觉痕)       Trace          → 纹
nail               → 钉(NailForge)   DAG            → 径网
Task               → 程              Pass Criteria  → 至迹(达到的迹)
ConsumeLog         → 取纹             schema         → 形
```

---

**本文 SoR · 不一致时以本文为准**
**改本文 = 通知 SPEC.md + README + 后续派单 spec**
