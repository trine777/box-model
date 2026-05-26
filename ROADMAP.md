# Box Model · 开发路线

> 一人公司级 KM 自用存储。目标:让 Box 能 dogfood 管理自己的开发进度,再逐步补全 KM 能力。

约定:
- **R** = Requirement(需求)。编号按依赖顺序,前置完成后才能动后置。
- **Nails** = 引用 NailForge `warehouse/` 路径。"无"表示直接编码,不走锻造工序。
- **AC** = Acceptance Criteria,逐条可验证才算交付。

---

## Phase 0 · 让 Box 具备 dogfood 条件

目标:跑通"写入 → 持久化 → 反复读取 → 修改 → 检索"最小闭环,使 Box 可被自身使用。

### R0.1 拆掉 `GetItem` 的自动 `MarkConsumed`

- **背景**:当前 `service.go:138` 每次 `GetItem` 都把 item 从 `available` 翻 `consumed`,KM 反复读取会被破坏。
- **Nails**:无(语义修正,5 行)。
- **变更**:
  - `GetItem` 不再调用 `MarkConsumed`。
  - 把 consume 副作用挪到独立方法 `MarkConsumedExplicit(itemID)`,供消息队列类场景显式调用。
  - `RecordConsume` 保留(审计日志该写还是写)。
- **AC**:
  - [ ] `go test ./...` 全绿
  - [ ] 改 `TestStoreBrowseAndConsume`:连续 2 次 `GetItem` 后,item.Status 仍是 `available`
  - [ ] 新增 `TestExplicitMarkConsumed`:显式调用 `MarkConsumedExplicit` 才会翻状态

### R0.2 Item 可更新 / 版本链

- **背景**:进度类条目天然要修改(状态变更、增补内容),Box 当前只能 insert + 幂等返回。
- **Nails**:无(数据模型扩展)。
- **变更**:
  - `Item` 增字段 `RevisionOf string`(指向前一版本 `item_id`)+ `Latest bool`。
  - 新方法 `ReplaceItem(callerID, prevItemID, StoreRequest) (Item, error)`:旧 item 标 `superseded` 且 `Latest=false`,新 item `Latest=true` 并指回旧 ID。
  - `Browse` 默认只返回 `Latest=true`,加 filter `IncludeHistory bool`。
  - 加新方法 `UpdateLabels(itemID, labels)`(纯标签更新,不开新版本)。
- **AC**:
  - [ ] `TestItemRevisionChain`:store → replace → browse 默认只见新版本;`IncludeHistory=true` 见全链
  - [ ] `TestUpdateLabelsNoRevision`:`UpdateLabels` 后 `Latest` 不变、`item_id` 不变
  - [ ] schema.sql 更新 `box_items`:加 `revision_of TEXT NULL`、`is_latest BOOLEAN NOT NULL DEFAULT true`,加 `idx_box_items_latest`

### R0.3 `FileStore` 本地持久化

- **背景**:`MemoryStore` 进程一停就没,无法跨会话管进度。一人公司不必上 Postgres,JSON 文件足够。
- **Nails**:无(直接落盘)。设计参考 `data_lake_organizer` 的 Bronze 层思路:每条 item 一个文件,目录按 box 分。
- **变更**:
  - `box/file_store.go`:实现 `Store` 接口,数据落 `~/.box/<box_key>/items/<item_id>.json` + `~/.box/<box_key>/box.json`。
  - 启动时扫描目录恢复内存索引(`byBox` / `byIdem`)。
  - 写入用临时文件 + rename,保证原子性。
  - 加 `TestFileStorePersistAndReload`:store → close → 新实例 → browse 仍能命中。
- **AC**:
  - [ ] `~/.box/<key>/` 目录结构清晰,人肉可读
  - [ ] kill 进程后重启,数据无丢失
  - [ ] `TestFileStorePersistAndReload` 通过
  - [ ] `MemoryStore` 测试照旧通过(共用 `Store` 接口)

### R0.4 `box` CLI

- **背景**:没有客户端,无法从 shell 操作。
- **Nails**:参考 `warehouse/data_engineering_forge/data_api_builder` 的 `a1→a2→a3→a4` 工序(析需求 → 设计 schema → 生成代码 → 校验),但因仅做 CLI 不做 HTTP,**只借鉴 a1/a2 设计思路,a3/a4 直接 Go 编码**。
- **变更**:
  - `cmd/box/main.go`,基于 stdlib `flag` 不引外部依赖。
  - 子命令:`init` / `store` / `browse` / `show` / `replace` / `tag` / `summary` / `seal`
  - 默认 store 是 `FileStore`,根目录读 `BOX_HOME` 环境变量,缺省 `~/.box`
  - 标志:`--label k=v`(可多次)、`--ref k=v`、`--kind`、`--storage`、`--content @file.json` / `--content -`(stdin)
- **AC**:
  - [ ] `box init dev-progress --owner=trine` 创建 Box
  - [ ] `box store dev-progress --kind=task --label __sem:topic=phase0 --storage 'folder://./notes/r0.1.md' --content '{"status":"in_progress"}'` 成功
  - [ ] `box browse dev-progress --label __sem:topic=phase0` 输出至少 1 条
  - [ ] `box show <item_id>` 显示完整 item(且不会副作用翻状态,依赖 R0.1)
  - [ ] `box replace <old_item_id> --content '...'` 后 `browse` 默认只见新版本
  - [ ] 退出码:成功 0,validation 错 2,not found 4,conflict 5

---

## Phase 1 · 真正开始 dogfood

目标:把 Box Model 自身的开发活动迁入 Box,替代 `ROADMAP.md` 的纯文件管理。

### R1.1 建 `dev-progress` Box 并迁入本文件的需求列表

- **依赖**:R0.1-R0.4 全部完成。
- **Nails**:无(直接用 CLI)。
- **变更**:
  - `box init dev-progress`
  - 每条 R0.x / R1.x / R2.x... 写一条 item:
    - `kind=requirement`
    - `storage_uri=repo://github.com/trine777/box-model@<sha>:/ROADMAP.md#R0.1`
    - `__op:phase=0|1|2|3`、`__gate:status=done|in_progress|todo`、`__sem:topic=mark_consumed|persist|cli|...`
    - `metadata.ac_count` / `metadata.ac_done`
- **AC**:
  - [ ] `box browse dev-progress --label __gate:status=done` 至少 4 条(R0.1-R0.4)
  - [ ] `box summary dev-progress` 显示 by_kind / by_label 统计
  - [ ] `ROADMAP.md` 改为指向 Box 的入口说明,正文留在 Box 里

### R1.2 把现有 docs/ 与 schema.sql 作为 item 索引

- **依赖**:R1.1。
- **Nails**:无。
- **变更**:每份 `docs/*.md` 和 `docs/schema.sql` 写一条 item,`storage_uri=repo://...`,`__sem:topic=design`,`__op:component=architecture|api|operations|schema`。
- **AC**:
  - [ ] `box browse dev-progress --label __sem:topic=design` 至少 4 条
  - [ ] 修改 `docs/architecture.md` 后,跑一次 `box replace` 写入新 content_hash,旧版本仍可 `--include-history` 见到

---

## Phase 2 · KM 能力补强(按价值排序,不必全做)

### R2.1 向量召回(chunk + embedding)

- **背景**:KM 的核心动作是"按意图找 item",exact filter 不够。
- **Nails**:
  - 参考 `warehouse/system_core/skill_retriever`(b1→{b2‖b3‖b4}→b5→b6 倒排索引模式),但**只借鉴召回链路设计**,索引方式改向量。
  - 数据模型参考 `data_warehouse_modeler` 的 `a2 design_dimensional_model` 工序输出维度表(`box_chunks` / `box_embeddings`)。
- **变更**:
  - 新表/文件:`box_chunks(item_id, ord, text, char_start, char_end)`、`box_embeddings(chunk_id, model, vector[])`。
  - `Service.Embed(itemID)`:取 `content`(或解析 `storage_uri` 内容)、chunking、调外部 embedding API(OpenAI/本地)、落 `box_embeddings`。
  - `BrowseFilter` 加 `VectorQuery []float32`、`VectorTopK int`,内存版用余弦相似度暴力算。
- **AC**:
  - [ ] 灌 10 条 item 后,`box browse dev-progress --semantic "持久化怎么设计的"` 能把 R0.3 排到前 3
  - [ ] `TestVectorRecallRanking`:已知 query 的 ground-truth top-1 命中
  - [ ] embedding model 名记录在 metadata,便于换模型重建

### R2.2 全文 / BM25 召回(混合检索)

- **依赖**:R2.1。
- **Nails**:`warehouse/system_core/skill_retriever` 的倒排索引部分**直接借鉴**(b1 build_inverted_index / b3 search_by_keyword / b4 rank_by_relevance 三步可直接对应实现)。
- **变更**:
  - 内存倒排索引(token → item_id set + tf-idf)。
  - `VectorQuery` 和 `KeywordQuery` 都给定时,做加权融合(默认 `0.6*vector + 0.4*bm25`)。
- **AC**:
  - [ ] `box browse --keyword "MarkConsumed"` 精确命中 R0.1
  - [ ] `box browse --semantic "..." --keyword "..."` 输出按 hybrid 排序

### R2.3 Postgres Store(可选,当 item > 1万 再做)

- **背景**:`docs/schema.sql` 已就绪,GIN 索引现成。一人公司初期文件存够用。
- **Nails**:
  - `warehouse/data_engineering_forge/data_warehouse_modeler`(a1→a2→a3→a4 全流程):a3 输出 PG DDL、a4 校验模型质量。
  - `warehouse/data_engineering_forge/data_quality_guardian` 用于建表后跑数据质量校验。
- **变更**:
  - `box/postgres_store.go`,实现 `Store` 接口
  - Browse 的 label/source_ref 走 GIN
  - 加 migration 工具:`box migrate from-files --to-postgres dsn://...`
- **AC**:
  - [ ] `TestPostgresStore` 在本地 PG 跑通(可放 `//go:build integration`)
  - [ ] 从 FileStore 全量迁到 PG 后 `box summary` 数字一致
  - [ ] label browse 在 10万 item 数据集 p95 <= 200ms(operations.md 给的 SLO)

---

## Phase 3 · 治理与运维(按需启用)

### R3.1 TTL / expire 后台 job

- **背景**:schema 留了 `expired` 状态但无实现。
- **Nails**:
  - 参考 `warehouse/data_engineering_forge/data_lake_organizer` 的"数据生命周期策略配置"职责(`a3 generate_lake_config` 输出生命周期规则)。
- **变更**:
  - `StoragePolicy` 加 `MaxAge time.Duration` / `MaxAgeByKind map[string]time.Duration`。
  - 新方法 `Service.RunExpiration(ctx)`,定期扫描标记 `expired`,Browse 默认过滤掉。
  - CLI:`box expire dev-progress --dry-run` / `--apply`。
- **AC**:
  - [ ] `TestExpiration`:`MaxAge=1ms` + sleep 后,扫描会把 item 标为 `expired`
  - [ ] `--dry-run` 只输出待过期清单不改状态

### R3.2 可观测性

**SoR**:见 `docs/observability.md`(指标体系 + 日志 + 埋点全表)。本次拆两阶段:

#### R3.2.1 MVP(优先级提到 R0.5 之前)

- **Nails**:参考 `warehouse/data_engineering_forge/data_observability_setup`(只借鉴指标分类思路,不做完整 SLO/告警)。
- **变更**:
  - 新建 `box/obs/` 包:`Observer` 接口 + `NoopObserver` + `MemObserver`(内存 Counter + slog JSON 日志到文件)
  - `Service` 改用 option pattern:`NewService(store, opts...)`,加 `WithObserver(obs)`
  - 在每个 Service 方法埋点(`box/item/store/cli` 四个 domain)
  - FileStore journal-replay 走 warn 日志
  - CLI 加 `box stats` / `box logs`
  - 配置:`BOX_OBS_DISABLED` / `BOX_LOG_PATH` / `BOX_LOG_LEVEL`
- **AC**:
  - [ ] NoopObserver 跑测试 0 alloc(`testing.AllocsPerRun`)
  - [ ] 端到端:用 MemObserver 跑 CreateBox+Store+Browse+ReplaceItem+Delete,Snapshot 关键 counter > 0
  - [ ] 错误分类正确:4 种 err_type counter 各对得上
  - [ ] `box stats` 显示 counter + timer avg/count
  - [ ] `box logs --tail 10` 能解析最近 10 行
  - [ ] 老 57 测试零回归
  - [ ] CLI 不传 obs 时跑 NoopObserver(`BOX_OBS_DISABLED=1` 也走相同分支)

#### R3.2.2 进阶(后续)

- Histogram + 真分位数
- 日志按日轮转
- OpenTelemetry trace
- Prometheus exposition

---

## Phase 4 · 对外接口(看是否需要)

### R4.1 HTTP API

- **背景**:`docs/api.md` 是规范不是代码,如果想从浏览器/其他 host 访问需要它。一人公司本地可推迟。
- **Nails**:
  - `warehouse/data_engineering_forge/data_api_builder` 整套(a1→a2→a3→a4):
    - a1 extract_api_requirements → 把 `docs/api.md` 拆成需求清单
    - a2 design_api_schema → 输出 OpenAPI
    - a3 generate_api_code → 生成 handlers/router
    - a4 validate_api_quality → 接口质量审查
- **变更**:
  - `cmd/box-server/main.go`,基于 stdlib `net/http`
  - 路由对齐 `docs/api.md`
  - Body 限制 + 标签大小限制
- **AC**:
  - [ ] `curl -X POST .../boxes` 创建成功
  - [ ] `curl '.../boxes/{id}/items?label.__sem:topic=phase0'` 返回 R0.x
  - [ ] OpenAPI yaml 与实际路由一致(`openapi validate`)

### R4.2 鉴权(一人公司极简版)

- **背景**:一人公司不做多租户,但 HTTP 暴露后总得防"裸开"。
- **Nails**:无(用静态 token 足够)。注意 `auth_flow_builder` 已 deprecated,不要用。
- **变更**:
  - 单一 `BOX_API_TOKEN` 环境变量,所有写请求 `Authorization: Bearer ...` 校验
  - 读请求可配置 `BOX_READ_REQUIRES_AUTH=true|false`,默认 `false`(本地)/`true`(远端)
- **AC**:
  - [ ] 缺 token → 401
  - [ ] 错 token → 403

---

## 不做清单(明确划掉)

- 多租户 / RBAC / ABAC — 一人公司无意义。
- `auth_flow_builder` / 复杂用户体系 — 同上 + 该家族已 deprecated。
- 分布式 / 多 region — 单机文件 / 单 PG 实例足够。
- `backend_code_forge` 元家族 — 上下游 handoff schema 错位且审计未过,不当主流程引擎。
- Astrolabe 集成 — R2.1/R2.2 把召回内建后,Astrolabe 不再必要。

---

## 当前状态(手动维护到 Phase 1 完成后迁入 Box)

| 需求 | 状态 | 备注 |
|---|---|---|
| R0.1 拆 MarkConsumed | **done** | 6/6 测试绿,留 4 条架构债 |
| R0.2 版本链 | **done** | 21/21 测试绿;D#1 已清;P#1 已 patch;新增 D#5/D#6/D#7 |
| R0.3 FileStore | **done** | 33/33 测试绿(原 21 + Delete 4 + FileStore 8);D#7 清掉;D#4 调整为 won't-fix-unless-triggered |
| R0.4 CLI | **done** | 57/57 测试绿;`cmd/box` 端到端可跑;ListBoxes 污点已清(patch);新增 D#10 |
| R0.5 ListConsumes/MergeLabels/RemoveLabels/GetBox + history-guard + IdemKey 释放 + CLI Service 解耦 | **done** | 85/85 测试绿;清 D#2/D#5/D#6/D#9/D#10 共 5 债;走 `code_forge` 器官钉工序 |
| R3.2.1 观测 MVP | **done** | obs 包 + 11 verbs 全埋点 + `box stats/logs` + 68/68 测试;新增 D#11/D#12 |
| R0.6.1 Box 升级为内容存储 | **done** | 94/94 测试;`StoragePolicy.MaxContentBytes` 默认 256KB;不变式 #5 改写 |
| R0.6.2 `import-nailforge` 工具 | **done** | 102/102 测试;3 次跑真 NailForge 幂等;892 nails 入库;Box 核心未污染 yaml |
| R0.7.1 符号引擎 AI-facing | **done** | 122/122 测试;Symbol 25 条原生符号 + 6 SymbolKind;Trace/Neighbors/LegendOf API;`__symbols__` bootstrap 幂等 |
| R0.7.2 CLI 符号操作 | **done** | 144/144 测试;SLP parser + 11 store flag + 3 新子命令 + bootstrap 自动调 + notation/explicit 互斥;第一次显式标 nail 序列的 spec |
| R0.7.3 基础视图(human-facing) | **done** | 160/160 测试;`box/view/` 包 + 3 渲染器 + cmdView/cmdRotate + 管道 `--stdin` + isTTY 检测;view 输出非 JSON 硬约束验证通过 |
| R0.7.4 关系视图 | **done** | 173/173 测试;graph LR / tree TD / mindmap 三 mermaid renderer;tree 严格过滤为 `<`/`⊃` hierarchy 关系;rotate --axis=relation → graph |
| R0.7.5 matrix 视图 | **done** | 181/181 测试;`kind × status` 11×7 二维表 + Total 行列;matrix 不通过 axis 进(决策 #2 验证);新增 D#14 |
| R0.7.6 import-nailforge 升级灌符号 | **done** | 193/193 测试;真 NailForge 522 item updated 补齐符号(版本链:v1→v2);`trace --domain="nf:构"` 命中 53 nails;agent 发现 atom 是 map 加 parseAtomMap 桥接 |
| R0.8.1 MCP server 部署 | **done** | 201/201 测试;17 tools (`box_*` 前缀) via `github.com/modelcontextprotocol/go-sdk v1.5.0`;box 核心未被 SDK 污染;in-memory transport 测试 + 真 stdio E2E 通;`docs/mcp.md` 写好 |
| R0.10 操作层(综合:storage-only + vehicle + pass_criteria + nail_chain + trace jsonl) | **done** | 224/224 测试;CreateTask/SetItemSymbols/AppendTaskTrace/ListTaskTrace 4 Service 方法;5 新 MCP tools(总 22);trace.jsonl 跨进程持久化;不变式 #10 写入 README;TestBoxDoesNotInterpretPassCriteria 验证 box=dumb storage;新增 D#16 |
| R0.10 v2 schema + R0.13.1 程辙层 | **done** | 239/239 测试;4 新 MCP tools(22→26);程符 `sync.Map` + 自动 trace 注入;crash-orphan startup hook;不变式 #11/#12 入 README;`TestPathLedgerFinishNotFrozen` 验 path ledger 真实施(FinishYiCheng 后 SetItemSymbols 仍能 flip ✓→→);dev-progress dogfood 实施过程(parent + sub-task + 12 trace events)|
| R0.13.2 trace 解耦 kind=task / pass_criteria 取消白名单 | **done** | 234 测试;Service.AppendEvent/ListEvents 取代 AppendTaskTrace/ListTaskTrace,作用于任意 item;PassCriteria → json.RawMessage(opaque);MCP 28→26(删 box_create_task / box_get_task / box_set_task_status;加 box_set_item_symbols;改名事件二将);`TestAppendEventToAnyItemKind` + `TestPassCriteriaIsOpaqueJSON` 验新契约;task 现为约定不是特权(符合不变式 #10) |
| R0.X 跨 box 引用统一 | **backlog** | SymRelation.Ref 支持 `item://<box>/<id>` 跨 box URI;Neighbors 跨 box BFS;CLI 暴露 SetItemSymbols;task ⊃ has_part 嵌套真支持;Demo 已 workaround 验证可行(见 dev-progress R0.13.1 task + sub-task) |
| R0.14 bulk ingest API | **backlog** | `box_store_batch` MCP tool;一次 RPC ≤ N items(默认 100);原子性边界:每 item 独立 idem_key,失败的不回滚已成功的,返回 per-item result;CLI `box import --jsonl <file>`;场景:一本书拆 chunks 灌入、批量同步 docs/ |
| R0.15 query predicates | **backlog** | `box_browse` 加 `label_predicates` 字段:`{key, op:eq/ne/gt/lt/contains, value}`;`box_trace` 加 `compound`(AND/OR 多组 SymbolQuery);**仍不做** 全文 / 类型推断 / 跨字段表达式 — 那是 R2.x;目的是少几次 client-side filter |
| R0.16 item watch / subscription | **backlog** | MCP `resources` + `listChanged` notify item-level 变更;轮询桥(`box_watch_box --since=<ts>` 返回 since 后的事件);场景:多 agent 协作时一边写一边看;**不做** WebSocket(MCP 已有 SSE 信道) |
| R1.1 dev-progress Box | **done** | 14 条需求灌入 `~/.box/boxes/dev-progress/`;版本链 dogfood 验证通过(R1.1 v1→v2) |
| R1.2 迁入 docs | blocked | 等 R1.1 |
| R2.x KM 召回 | future | |
| R3.x 治理运维 | future | |
| R4.1 + R4.2 远端 MCP + Bearer auth + fly 部署 | **done** | 28 tools (26→28: +box_manual +box_legend_all);Streamable-HTTP transport via SDK NewStreamableHTTPHandler;Bearer middleware constant-time;Dockerfile multi-stage distroless 3.9 MB;fly app `box-mcp-trine` @ nrt + box_data 1GB Volume;endpoint https://box-mcp-trine.fly.dev/mcp;/healthz 200;无 Bearer 401;远端 initialize + tools/list + box_manual + box_legend_all 全通 |
| R4.x HTTP 后续 | future | 解 D#3(caller != consumer);多 token / 多 agent 分权(R4.2 v2);Postgres store(R2.3) |

---

## 架构债清单(随实现累积,每条标 owner/拟解决需求)

| ID | 描述 | 影响 | 拟解决于 |
|---|---|---|---|
| D#1 | `Service.Consume` 用 `item.Status = "consumed"` 手工赋值,而非 re-fetch | 字段增多时易漏 | R0.2 |
| D#2 | 测试直接读 `MemoryStore.consumes` 私有字段 | 换 Store 实现时测试失效 | R0.5 |
| D#3 | `ConsumerID == callerID`,无代理消费支持 | 鉴权层无法承载 agent-on-behalf | R4.x |
| D#4 | `RecordConsume + MarkConsumed` 非原子 | 跨文件/跨表时一致性风险 | **won't fix unless triggered**(FileStore 下顺序非原子,接受;ReplaceItem 已 journal 化,Consume 若未来要原子化按同模式包装即可) |
| D#5 | `UpdateLabels` 允许 patch 历史版本(IsLatest=false),应默认禁止 | KM 检索语义模糊 | R0.4 后 |
| D#6 | `UpdateLabels` 是完整替换,缺 `MergeLabels` 便利方法 | 加一个标签得先 Get 全集 | R0.5 |
| D#7 | 无 `DeleteItem` 方法,但 `Status="deleted"` 已定义 | 删数据无 API | **done @ R0.3**(`Service.DeleteItem` + `Store.DeleteItem` 实现,owner 校验 + ErrConflict on re-delete) |
| D#8 | `consumes.jsonl` 无轮转/上限,长期会无限增长 | 单文件膨胀,启动加载慢 | R3.1 顺手做 |
| D#9 | `DeleteItem` 后 `IdemKey` 仍占在 `byIdem`,新 Store 同 IdemKey 会返回 deleted item | KM "删了再重新加同名" 场景失败 | R0.5 |
| D#10 | CLI 9 处 `st.Xxx` 直接调 Store 绕过 Service(cmdReplace/Tag/Delete/Consume/Seal 的 caller 解析逻辑) | Service 层不能强制鉴权/校验;未来 PostgresStore 接入时易漏 | R0.5 |
| D#11 | `FileStore.store.open.*` 指标是 dead code:`SetObserver` 在 `OpenFileStore` 之后才能调,构造期 observer 永远是 Noop | 启动期指标永远 0,运维不可见 | R3.2.2(改 `OpenFileStore` 加 option 参数,或包级 default observer) |
| D#12 | `Observer.Timer` 接口暴露但 Service 全用 `Observe(duration_ms)`,Timer 0 调用方 | 接口冗余;`box stats` 输出"timers"段永空 | R3.2.2 一并清(或加 histogram 时改用 Timer) |
| D#13 | `go build ./cmd/<X>` 不带 `-o` 时会在仓库根落同名 binary;每加一个 cmd 都需补 .gitignore(R0.6.2 / R0.8.1 都触发过) | 误 commit | .gitignore 已含 4 个 cmd;长期改:Makefile 强制 `-o bin/` 或 `.gitignore` 加通配 `/box*` |
| D#14 | matrix 视图列宽固定 5 但 `(none)` header 6 字符,导致 `(none)Total` 紧贴影响可读性 | UX 小瑕疵,数据无误 | 试过单改 cellWidth=7 但回归 5 个 matrix 测试(divider 长度断言);留 follow-up:加 `cellWidth := max(5, max(len(headers)))` 自适应 + 同步更新 divider 长度生成 + 改 5 个断言 |
| D#15 | `Service.Trace` 多维 SymbolQuery 似乎不命中(同 box 内 store 后 trace --kind=status --value=? 返 0)| `box.trace` API 反查不可靠,影响 task agent 自验 goal | R0.10.x — 应优先做(影响 task pass_criteria 检查工作流) |
| D#16 | `TraceStep.Step` 字段 `omitempty`,导致 step=0 在 JSON 输出里被去掉 | trace 列表第 1 条无 step 字段,人眼可能困惑 | 改 `Step int` 为 `Step int \`json:"step"\`` 去 omitempty(小修)|
| D#17 | (置空,合并入 R0.10 v2 spec)| | |
| D#18 | `import-nailforge` 只灌 `versions/` 老结构,不识别 NailForge 新结构 `active/action_nails/` | 新建 forge(database_engine_forge / backend_code_forge / code_forge)的 nail 全没进 nail-index-real | R0.X 顺手做:升 import-nailforge classifier 兼容 `active/action_nails/*.yaml` |
| D#19 | CLI 缺 `box set_item_symbols` 命令,task ⊃ has_part / SymRelation 修改需直接走 MCP / Service | task 嵌套 / 关系图修订没 CLI 路径 | R0.X 顺手:CLI 加 `box set_item_symbols <id> --sym=...` |
