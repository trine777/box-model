# 观察体系 v2 技术方案 SPEC（R15）

> 锻造 nail：`saas_builder/architecture_forge`（系统边界 / 模块 / 数据 schema /
> 采集契约 / 非功能）。**这是开发前置物：spec 评审通过后才进入实现。**
>
> 背景：v1（R14）的五元素/觉痕符号化仪表盘被判"完全不可用"——它是哲学花架子，
> 不反映工程现实。v2 推翻符号抽象，只记**真实物理 + 业务指标**：机器容量、task
> 执行完整度、性能（请求）、box 使用次数与更新频率。**Fly 不纳入观察舰队。**

## a1 · 系统边界与目标

- **目标**：运维可读的工程指标记录 —— 回答"机器还撑得住吗 / task 跑得完吗 /
  哪个操作慢或错 / 哪个 box 在被用"。给数字，不给符号。
- **舰队**：`:7777`（业务核心）+ `:7788`（观察平面）两实例。**Fly 备份排除**
  （用户明确：fly 不用）。两实例同机，故机器级指标（磁盘/进程）按 host 共享。
- **不做**（本期）：告警路由、阈值升级、根因自动化。先把"记录 + 可观察"做扎实。
- **切自指**：采集写入只落观察平面 `:7788`，不污染业务 counter。

## a2 · 指标模型（四类。每条标 数据源 / 单位 / 可行性）

### 容量 capacity（机器物理）
| 指标 | 单位 | 数据源 | 可行性 |
|---|---|---|---|
| disk_used / disk_total | bytes | `df`（本机 shell） | ✓ 现成 |
| box_home_bytes（~/.box, ~/.box-obs） | bytes | `du -sb` | ✓ |
| blob_count / blob_bytes | n / bytes | find ~/.box blobs | ✓ |
| proc_rss / proc_cpu / proc_uptime | MB / % / s | `ps`（本机） | ✓ |

> 约束：shell 指标只能在**本机**采（Fly 不采，正合舰队范围）。

### task 执行 tasks
| 指标 | 数据源 | 可行性 |
|---|---|---|
| total / by_status(✓→✗?~◯) | `box_trace kind=T` 或遍历各 box kind=T | ⚠ 待联调（trace 查 task 接口本轮报错，开发第一步验证） |
| completion_rate（完整度＝✓/total） | 由 by_status 计算 | ✓（依赖上行） |
| stuck（→ 状态且 last_event > 24h） | task item 的 events 末事件时间 | ⚠ 需读 `box_list_events` 末事件 |
| duration（task_start→task_finish） | events 时间差 | ⚠ 需 events 有时间戳 |

### 性能 perf（请求）
| 指标 | 数据源 | 可行性 |
|---|---|---|
| per-op calls / success / error / err% | `box_observability` counters（24个，真实） | ✓ 现成 |
| err_type 分布 | counter 的 `\|err_type=` 标签 | ✓ |
| 延迟 avg_ms / p95_ms | `box_observability` timers | ✗ **当前 timers 为空**（见 a3 缺口①） |

### 业务 business
| 指标 | 数据源 | 可行性 |
|---|---|---|
| item 总数 / per-box item 数 | `box_summary` per box | ✓（需遍历 12 box） |
| box 数 / by-sphere | `box_globes` | ✓ |
| **box 使用次数（per-box）** | — | ✗ **缺口②**：box_observability 是全局 op counter，不分 box |
| **更新频率** | `box_summary.latest_stored_at` / events | ⚠ 仅"最近一次"，频率需快照序列差分 |

## a3 · 数据源缺口与决策（spec 的核心 —— 不解决就别开发）

- **缺口①：延迟 timers 为空。** 决策待选：(A) 确认 `box/obs` 是否记 duration_ms
  timer、为何快照空（可能 Snapshot 未导出 timer）→ 修导出；(B) 若代价大，本期
  延迟指标**暂不纳入**，spec 标注"延迟 = 后续项"。**默认 A**（先查 5 分钟，能修则修）。
- **缺口②：per-box 使用次数无现成源。** 决策待选：
  (A) 用 `box_list_consumes` 审计日志聚合 per-box 读次数（若 consume 覆盖读路径）；
  (B) 在 `box/service.go` 给 obs 埋点加 `box_id` 标签（counter 维度化），最准但要改埋点；
  (C) 本期用 per-box item 数 + latest_stored_at 作为"活跃度"近似，使用次数标为后续。
  **倾向 C 起步、B 为正解**——请评审定。
- **更新频率**：靠快照序列差分（本期快照间 item 数增量 / 时间），需采集器累计。

## a4 · 采集器设计（boxsnap 重写）
- 输入：`BOX_FLEET`（默认 `:7777,:7788`，**无 Fly**）、`BOX_OBS_ENDPOINT`（:7788）。
- 每实例采：perf（box_observability）+ business（globes/summary 聚合）；
  机器级（df/du/ps/blob）按 host 采一次。
- 输出：写观察平面 box `obs-metrics`（新 box，scope:ops），切自指。
- 频率：launchd `com.box-metrics` timer（已存，重指向）。

## a5 · 落库 schema（obs-metrics box，item content）
```json
{
  "instance": "100.83.33.126:7777",
  "host": "100.83.33.126",
  "taken_at": "20260529T080000Z",
  "capacity": {"disk_used":, "disk_total":, "box_home_bytes":, "blob_count":, "blob_bytes":, "proc_rss_mb":, "proc_cpu":, "proc_uptime_s":},
  "tasks":    {"total":, "by_status":{}, "completion_rate":, "stuck":},
  "perf":     {"ops":[{"op":,"calls":,"errors":,"err_pct":,"err_types":{},"avg_ms":,"p95_ms":}]},
  "business": {"box_count":, "item_total":, "per_box":[{"key":,"items":,"latest_stored_at":,"uses":}]}
}
```
kind=O，source_type=metrics，storage_uri=`row://obs-metrics/<instance>/<ts>`，idem_key 防重。

## a6 · 展示（重写为真实数字表，零符号抽象）
- `cmd/box-mcp/dashboard.go`：HTML **数字表** —— 容量卡 / task 卡（含完整度%、卡住数）/
  性能表（op·calls·err%·avg-ms 高错率标红）/ 业务表（per-box items·使用·更新）。
- `boxops`（agent 面 CLI）：同数据纯文本表。
- `boxboard`（人面）：终端表 + 本地 HTML（绕代理）+ markdown 留痕。
- 多实例：每实例一段；缺席的期望成员标 down。

## a7 · 非功能
tailnet 零 token（ProxyHandler({})）；进程 counter 重启清零→趋势靠 obs-metrics 快照
序列；shell 指标本机限定；切自指。

## a8 · 旧码治理（"无关代码注意治理"）
v1 符号化产物按方向否定清理：
- **删**：`scripts/boxlife`（五元素）、`scripts/boxtrend`（五元素节律）、`scripts/boxstate`（觉痕画像）。
- **重写**：`cmd/box-mcp/dashboard.go`（五元素→数字表）、`scripts/boxsnap`（→四类指标）、
  `boxops`/`boxboard`（→数字表）。
- **保留**：`scripts/boxstat`（本就是精确数字表，方向正确）、`scripts/boxls`/`boxput`/`boxget`/`boxcall`/`boxlint`（与观察无关）。
- `docs/observability.md`：标记 v1（五元素）为 superseded，指向本 spec。

## a9 · 验收标准（pass_criteria 细化）
1. obs-metrics box 有 :7777 + :7788 两实例的真实快照（capacity/tasks/perf/business 四类齐）。
2. dashboard/boxops 显示真实数字：磁盘%、task 完整度%、至少一个 op 的 calls/err%、box item 数。
3. 五元素/觉痕代码已按 a8 治理（grep 无残留五元素渲染）。
4. 缺口①②有明确落地（修了 / 或 spec 标注为后续且代码不假装有该指标）。
5. dev-progress 留 topic=r15-obs-engineering release note。

## a3.5 · 数据源诊断锁定（开发依据 · 已评审）

诊断 `box/obs/metrics.go` + `box/service.go` 后，三决策已定：

- **缺口①（延迟）— 误判，已澄清**：延迟走 `Observe("box.X.duration_ms")` →
  `SnapshotSummary.Observed[key] = TimerStats{count,avg_ms,min_ms,max_ms}`，**现成**
  （boxstat 已用 avg）。"timers 为空"只因延迟不走 `Timer()`。**p95 缺口**：
  `TimerStats` 无 p95。决策(用户：能修则修)→ **修**：`TimerStats` 加 `P95Ms`，
  `statsFromFloats`/`statsFromDurations` 排序取 p95（小改，单测覆盖）。
- **缺口②（per-box 使用次数）**：决策(用户：埋点最准)→ `box/service.go` 给
  **`item.*` 操作**埋点 tags 加 `box_id`（item.store/get/browse/replace/consume/
  set_symbols/append_event）。`box.*`/`event.*` 级不加（无 per-box 意义）。
  **协调**：`keyFor` 已支持多 tag；`boxstat` 按 op 聚合时须 **strip `box_id`**
  避免 key 爆炸误读；per-box 聚合则按 `box_id` 分组。12 box 量级 key 可控。
- **task 深度**：决策(用户：全做)→ `box_trace kind=T` 拿 task items → by_status /
  completion_rate；`box_list_events` 拿 task_start/finish 时间戳 → duration；
  末事件 >24h 且仍 `→` → stuck。

> 数据源全部落定，无"采不到却假装有"的指标。进入实现（a4）。
