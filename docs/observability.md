# Observability 设计

> Box Model 一人公司级 KM 自用存储的观测体系。本文是埋点的 SoR(Source of Record)。

## 一、目标分层

| 层 | 问题 | 工具 |
|---|---|---|
| 业务洞察 | "我存了多少东西,在用什么 kind,什么 topic" | Counter / 业务事件日志 |
| 健康监控 | "系统跑得稳吗,有没有错" | Counter(error) / Timer / 错误日志 |
| 故障定位 | "刚才那个失败是为什么" | 结构化日志(level=warn/error) |
| 容量预警 | "存储要爆了吗" | gauge(disk_usage,留 R3.2.2) |

## 二、Observer 接口

```go
package obs

type Observer interface {
    Inc(name string, tags map[string]string)
    Timer(name string, tags map[string]string) func()  // returns stop func, defer call
    Observe(name string, value float64, tags map[string]string)
    LogInfo(msg string, kv ...any)
    LogWarn(msg string, kv ...any)
    LogError(msg string, err error, kv ...any)
}

// NoopObserver:零开销默认实现。所有 Service 方法默认注入此实现。
type NoopObserver struct{}

// MemObserver:本次 MVP 实现。
//   - Counters in-memory map[name|tagsKey]int64
//   - Timers append-only sample list per name|tagsKey
//   - Logger 用 log/slog JSON handler 写到文件
type MemObserver struct {
    mu       sync.Mutex
    counters map[string]int64
    timers   map[string][]time.Duration
    logger   *slog.Logger
    clock    func() time.Time  // injectable for tests
}

func NewMemObserver(logFile io.Writer) *MemObserver
func (o *MemObserver) Snapshot() Snapshot
func (o *MemObserver) Reset()
```

## 三、指标命名规范

- 全小写,层级用 `.` 分隔:`<domain>.<entity>.<verb>[.<outcome>]`
- 命名前缀固定:
  - `box.*` — Box(容器)级
  - `item.*` — Item 级
  - `store.*` — Store 层底层(FileStore journal 等)
  - `cli.*` — CLI 入口

## 四、Tag 设计原则

**允许的 tag**(低基数):
| Tag | 取值范围 |
|---|---|
| `kind` | 业务自定义,但典型 < 20 种 |
| `source_type` | 业务自定义,< 20 |
| `owner_type` | `room/area/user/standalone` |
| `err_type` | `validation/forbidden/notfound/conflict/internal` |
| `mark_consumed` | `true/false` |
| `consumer_type` | `room/agent/user/external` |
| `storage_scheme` | `row/blob/folder/repo/s3/ipfs/collection`(从 storage_uri 取前缀) |

**禁止的 tag**(高基数会爆指标空间):
- `box_id` / `item_id` / `caller_id` / `idem_key` / `storage_uri`(完整)

box_id/item_id 只进**日志结构化字段**,不进 metric tag。

## 五、Service 层埋点位置(SoR)

每个 Service 方法的埋点模板:

```go
func (s *Service) Xxx(...) (..., error) {
    stop := s.obs.Timer("item.xxx.duration_ms", tags)
    defer stop()
    s.obs.Inc("item.xxx.attempt", tags)
    
    // ... 业务逻辑
    
    if err != nil {
        tags["err_type"] = classifyErr(err)
        s.obs.Inc("item.xxx.error", tags)
        s.obs.LogWarn("xxx failed", "op", "xxx", "err", err.Error(), "box_id", boxID, "item_id", itemID)
        return ..., err
    }
    s.obs.Inc("item.xxx.success", tags)
    s.obs.LogInfo("xxx ok", "op", "xxx", "box_id", boxID, "item_id", item.ID, "kind", item.Kind)
    return ..., nil
}
```

完整埋点矩阵:

| Service 方法 | counter (attempt) | counter (success) | counter (error) | timer | tag keys |
|---|---|---|---|---|---|
| CreateBox | `box.create.attempt` | `box.create.success` | `box.create.error` | `box.create.duration_ms` | owner_type, err_type |
| GetBox | `box.get.attempt` | `box.get.success` | `box.get.error` | `box.get.duration_ms` | err_type |
| GetBoxByKey | `box.get_by_key.attempt` | `box.get_by_key.success` | `box.get_by_key.error` | `box.get_by_key.duration_ms` | err_type |
| SealBox | `box.seal.attempt` | `box.seal.success` | `box.seal.error` | `box.seal.duration_ms` | err_type |
| Store | `item.store.attempt` | `item.store.success` | `item.store.error` | `item.store.duration_ms` | kind, source_type, storage_scheme, err_type |
| Browse | `item.browse.attempt` | `item.browse.success` | `item.browse.error` | `item.browse.duration_ms` | err_type |
| Browse (extra) | — | `item.browse.result_count` | — | — | — |
| GetItem | `item.get.attempt` | `item.get.success` | `item.get.error` | `item.get.duration_ms` | err_type |
| ReplaceItem | `item.replace.attempt` | `item.replace.success` | `item.replace.error` | `item.replace.duration_ms` | kind, err_type |
| ReplaceItem (extra) | — | `item.replace.revision`(Observe) | — | — | kind |
| UpdateLabels | `item.update_labels.attempt` | `item.update_labels.success` | `item.update_labels.error` | `item.update_labels.duration_ms` | err_type |
| DeleteItem | `item.delete.attempt` | `item.delete.success` | `item.delete.error` | `item.delete.duration_ms` | err_type |
| Consume | `item.consume.attempt` | `item.consume.success` | `item.consume.error` | `item.consume.duration_ms` | consumer_type, mark_consumed, err_type |
| Summary | `box.summary.attempt` | `box.summary.success` | `box.summary.error` | `box.summary.duration_ms` | err_type |

`result_count` 用 `Observe` 而不是 `Inc`,记浏览结果数分布。`replace.revision` 同理记链深度。

## 六、FileStore 层埋点(异常信号关键)

| 位置 | metric | 级别 |
|---|---|---|
| `OpenFileStore` 开始 | `store.open.attempt` | — |
| `OpenFileStore` 完成 | `store.open.success` + `store.open.duration_ms` | — |
| `replayJournals` 发现 journal 文件 | `store.journal.replay` + LogWarn "replaying journal"(box_key, journal_count) | **warn** — 表示上次崩溃过 |
| `applyJournalToDisk` 失败 | `store.journal.apply_error` + LogError | error |
| `writeFileAtomic` 失败(IO) | `store.write.error` + LogError | error |

## 七、日志格式(slog JSON)

每行一条 JSON,字段顺序固定:

```json
{
  "time": "2026-05-23T10:15:23.412Z",
  "level": "INFO",
  "msg": "item stored",
  "op": "Store",
  "box_id": "box_xxx",
  "item_id": "item_yyy",
  "kind": "requirement",
  "storage_scheme": "repo"
}
```

错误日志额外:
```json
{
  "time": "...",
  "level": "WARN",
  "msg": "store rejected",
  "op": "Store",
  "err": "validation: format \"yaml\" not allowed",
  "err_type": "validation",
  "box_id": "box_xxx",
  "kind": "design"
}
```

**敏感数据约束**:
- 不打 `content`(可能含敏感)
- `storage_uri` 只取 scheme,不打全路径(避免泄漏 token-bearing URL)
- `idem_key` 不打(可能含业务标识)

## 八、文件出口

| 文件 | 用途 | 轮转 |
|---|---|---|
| `$BOX_HOME/_logs/box.log.jsonl` | 当前日志 | R3.2.1 不做,文件无限增长(后续按日轮转) |
| `$BOX_HOME/_metrics/snapshot.json` | 当前 counter / timer 摘要,程序退出 / `box stats --persist` 时写 | 覆盖式 |

## 九、配置(环境变量)

| 变量 | 默认 | 含义 |
|---|---|---|
| `BOX_OBS_DISABLED` | unset | 设为 `1` 切回 NoopObserver |
| `BOX_LOG_PATH` | `$BOX_HOME/_logs/box.log.jsonl` | 日志路径 |
| `BOX_LOG_LEVEL` | `info` | `debug/info/warn/error` |

`BOX_OBS_DISABLED=1` 时 CLI 也跑得动,只是没有任何观测产出。

## 十、CLI 入口

```
box stats [--name <pattern>] [--reset]
  默认:打印 counters + timer 简单 avg/count
  --name "item.*":只显示匹配前缀
  --reset:打印完之后清零(注意非线程安全,跑时别并发写)

box logs [--tail N] [--level warn] [--op Store] [--since 1h]
  默认:tail 50 行
  --level:只看 >= 该级别
  --op:filter op 字段
  --since:相对时间过滤(简化用 time.Parse,1h/24h/7d 支持)
```

## 十一、性能预算

- NoopObserver:**零开销**(struct{} 方法都是 no-op)
- MemObserver Counter:1 个 map lookup + atomic add ≤ 200ns
- MemObserver Timer:`time.Now()` 两次 + append slice ≤ 1µs
- 日志写文件:slog JSON handler buffered,fsync 仅在 close/snapshot 时
- 总目标:Service 方法埋点开销 < 操作本身的 1%

## 十二、测试策略

1. **NoopObserver 零分配**:`testing.AllocsPerRun` 验证 `obs.Inc(...)` 0 alloc
2. **MemObserver 计数正确**:并发 1000 次 Inc,Snapshot.Counters[name] == 1000
3. **Timer 记录条数正确**:N 次 stop(),Snapshot.Timers[name] 长度 == N
4. **日志 JSON 格式可解析**:每行 `json.Unmarshal` 通过,字段含 time/level/msg/op
5. **错误分类准确**:走 4 种错误(validation/forbidden/notfound/conflict) → counter `*.error` 各 +1,tag err_type 对应
6. **Service 埋点端到端**:用 MemObserver 跑一遍 CreateBox+Store+Browse+ReplaceItem+Delete,Snapshot 中关键指标全部 > 0
7. **CLI `box stats` / `box logs`** 跑通

## 十三、不做(R3.2.2)

- Histogram + bucket + p50/p95/p99 精确分位数
- 日志按日轮转
- OpenTelemetry trace 接出
- Prometheus exposition format
- 磁盘容量 gauge
- Alerting / threshold
