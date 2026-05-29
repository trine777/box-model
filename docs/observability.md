> **SUPERSEDED by docs/obs_v2_spec.md (R15)** — 五元素/觉痕观察已废弃, 改为工程化物理+业务指标。

# Observability 设计 — 符号化活化观察体系

> SoR for how box-model observes itself. box + agent = a **living system**;
> agent's continuous reporting is its metabolism. We don't bolt on
> Prometheus-style precise metrics — we observe the system in **its own
> symbol language** (觉痕 / priority / 五元素), fuzzily, for iteration
> decisions. (Supersedes the R3.2 precise-metric framing; that survives as
> the collection layer in §5.)

## 一、三轴

观察体系沿三个正交轴展开。

### 轴 1 · 深度(看多细)— 三视图,层层抽象

| 视图 | 工具 | 回答 | 符号 |
| --- | --- | --- | --- |
| 精确 | `boxstat` | "调了几次 / 错几次 / 多少 ms" | 数字(Prometheus 式,要查时) |
| 态 | `boxstate` | "健康吗 / 活跃吗" | 觉痕 `✓~✗◯` + priority `*/**/***` |
| 活 | `boxlife` | "系统在用哪种心智活着" | 五元素 风火土水空(fengyin SoR) |

### 轴 2 · 时间(瞬时 → 沉淀 → 趋势)

| 阶段 | 载体 | 工具 |
| --- | --- | --- |
| 瞬时 | `box_observability`(进程内 counter,重启清零) | boxstat/state/life 实时读 |
| 沉淀 | `metrics` box(快照持久化到 FileStore) | `boxsnap`(launchd `com.box-metrics` 每小时) |
| 趋势 | 历史快照序列 | `boxtrend`(五元素脉搏随时间 = **活化节律**) |

### 轴 3 · 符号语言(用什么刻画 — 体系的灵魂)

不另造符号,全复用 box 既有:

| 观察维度 | 复用的符号 | 同时也用于 |
| --- | --- | --- |
| 健康 | 觉痕 `✓~✗◯` | item 历史标记、task 状态 |
| 活跃 | priority `*/**/***` | item 优先级 |
| 活性/心智相位 | 五元素 风火土水空 | fengyin 心智能力分类 |
| 切片归属 | scope / domain 符号 | box 命名(R13)、nail 域 |

## 二、数据流 = 代谢闭环

```
agent 上报(代谢)
  → Service 埋点 obs.Inc / obs.Observe(采集,§5)
    → box_observability 内存 counter(瞬时)
      → boxsnap 每小时 → metrics box(沉淀;box 观察 box = dogfood 闭环)
        → 三视图消费:boxstat / boxstate / boxlife
          → boxtrend 读历史快照(趋势 = 活化节律)
```

## 三、四条自洽原则

1. **符号同源** — 观察用的符号 = 系统其他层用的符号。觉痕既标 item 历史
   又标系统健康;五元素既分类心智又刻画活性。一套符号语言贯穿
   **存储(符径)/ 执行(程辙)/ 状态(觉痕)/ 归属(scope)/ 活性(五元素)/
   观察**。观察不是新词,是已有符号的再投影。
2. **模糊优先** — 默认呈现模糊(觉痕/脉搏),精确数退 `--raw`。观察服务
   **迭代决策**,非监控告警(模糊数学:看趋势不看数)。
3. **dogfood 闭环** — 观察数据落进 box 自己(`metrics` box,scope:ops)。
   box 观察 box;观察是系统的一部分,不是外挂探针。
4. **代谢驱动** — 数据源 = agent 上报(活化系统的代谢),系统自产观察。
   没有 agent 活动 = 没有观察数据 = 系统静默(◯),这本身是真实信号。

## 四、工具谱(全在 scripts/,tailnet 零 token)

| 工具 | 轴 | 一句话 |
| --- | --- | --- |
| `box_observability` (MCP) | 采集出口 | 原始 counter/timer 快照 |
| `boxstat [prefix]` | 深度·精确 | 每操作 calls/err%/avg-ms 表 |
| `boxstate [--raw]` | 深度·态 | 觉痕健康/活跃画像 |
| `boxlife` | 深度·活 | 五元素活化脉搏 + 认知相位 |
| `boxsnap` | 时间·沉淀 | 快照落 metrics box(timer 驱动) |
| `boxtrend` | 时间·趋势 | 五元素脉搏随时间 = 活化节律 |

## 五、采集层(实现细节)

`box/obs/` 包:`Observer` 接口 + `NoopObserver` + `MemObserver`(内存
Counter + 可选 slog JSON 日志)。`Service` 每个动词埋点
(`box.*` / `item.*` / `event.*` 三域),记 `attempt / success / error
(+err_type 标签) / duration_ms`。`MemObserver.Snapshot()` 出
`SnapshotSummary`(counter + timer 聚合成 count/sum/avg/min/max),即
`box_observability` 返回的结构。

配置:`BOX_OBS_DISABLED` / `BOX_LOG_PATH` / `BOX_LOG_LEVEL`。

> 注:counter 是进程内累积,box-mcp 重启清零。**长期趋势靠 metrics box
> 的快照序列**(boxsnap + boxtrend),不靠 counter 本身。这是"沉淀层"
> 存在的理由。

## 六、读法指引(给 agent / 人)

- 日常一瞥:`boxlife`(系统活法)或 `boxstate`(健康)。
- 排查具体:`boxstat <prefix>`(精确数)。
- 看趋势:`boxtrend`(节律,需 metrics box 攒够快照)。
- 一个 ✗ / 一段持续沉寂(全 ◯)就是该迭代的信号 —— 模糊够用,不必精确。

## 七、双平面与人机协同观察(R14)

观察体系与业务体系**解耦为两个 box-mcp 实例**,避免观察写入污染业务
counter(自指),也让开发 agent 与运维 agent 上下文分离。

| 平面 | 实例 | 角色 | launchd |
| --- | --- | --- | --- |
| 业务平面 | `:7777` / `~/.box` | 核心服务(本机为主,Fly 为备份) | `com.box-mcp` |
| 观察平面 | `:7788` / `~/.box-obs` | 运维数据面(`obs-fleet` box 攒舰队快照) | `com.box-obs` |

数据流:`boxsnap`(v2)拉**业务平面** `box_observability` → 写**观察平面**
`obs-fleet` box(切自指)。三类消费者读观察平面,**同源符号**(觉痕/五元素):

| 入口 | 受众 | 形态 |
| --- | --- | --- |
| `boxops` | 运维 agent | CLI 符号:舰队五元素脉搏 + 觉痕健康 |
| `boxboard` | 人 | 终端 ASCII + 本地 HTML(`open file://`)+ markdown 留痕落 `obs-fleet` |
| `GET /dashboard` | 人(浏览器) | HTML 觉痕仪表盘,30s 自刷新,tailnet token-free |

**502 绕代理**:浏览器经系统代理(如 Clash/Surge 127.0.0.1:7890)直连 tailnet
IP 会 502(代理不路由 CGNAT 段)。`boxboard` 用 `ProxyHandler({})` 抓
`/dashboard` 写本地文件再 `open file://` —— 绕开代理,人可观察不被代理卡住。

> 锻造来源:本平面以真实钉 `data_engineering_forge/data_observability_setup`
> (a1 提取需求 → a2 设计策略 → a3 生成配置 → a4 验证覆盖)锻造,完工经
> `system_core/world_modeler` 落回 `box_operation_v3` 世界模型(符号同源原则
> 的元层兑现:观察体系自己也走 nail SOP + WM 落回)。
