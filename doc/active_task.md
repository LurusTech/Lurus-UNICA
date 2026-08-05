# Active Task: 修复分区表两处交付缺陷（去重唯一索引 + 分区枯竭）

## Context
上一增量部署时发现两处 schema 缺陷，会让 router 一接真实流量就出问题，已在本地 PG 16.14 复现：

1. `migrations/001` 最后一句 `CREATE UNIQUE INDEX idx_messages_platform_msg ON messages(platform_msg_id)`
   在分区表上缺分区键 `created_at`，PostgreSQL 直接拒绝
   （`unique constraint on partitioned table must include all partitioning columns`）。
   psql 逐句执行，前面全部成功、只有这一句失败，所以**入站去重的唯一索引在任何环境都不存在**，
   gateway 的 Redis 去重（fail-open + TTL）没有任何数据库兜底。
2. `messages` 分区只到 2026-04，`audit_logs` 只到 2026-06，当前月份写入直接
   `no partition of relation "..." found for row`。**audit_logs 同样已经写不进去**——
   写它的是 admin 的审计日志，上一增量没发现这一半。

根因不是"少建了几个分区"，而是**分区创建是一次性硬编码 + 依赖一个没人装的外部 cron**
（`scripts/maintain_partitions.sql` 只管 audit_logs，且注释写着"run monthly via cron"）。

## Critical Files
- `unica/router/migrations/001_core_schema.sql`
- `unica/router/migrations/012_partition_maintenance.sql`（新建）
- `unica/router/internal/state/partitions.go`（新建）
- `unica/router/internal/state/repository.go` / `manager.go`
- `unica/router/internal/routing/router.go`
- `unica/router/cmd/router/main.go`
- `unica/scripts/maintain_partitions.sql`

## 关键决策
- **去重索引改为「每分区唯一」**：分区表上的全局唯一约束必须含 `created_at`，
  而 `(platform_msg_id, created_at)` 恒不重复、等于没有约束。改在每个叶子分区上建
  `UNIQUE (platform_msg_id) WHERE platform_msg_id IS NOT NULL`，唯一性窗口 = 一个自然月。
  跨月边界的重投会漏，但平台重投窗口是秒到分钟级，接受并写进注释。
- **索引一旦存在，写入路径必须同步改**：否则重复消息让 `CreateMessage` 报错，
  而 router 在 state manager 出错时是 `ack` 掉的——等于静默丢消息，比现状更糟。
  用 `ON CONFLICT DO NOTHING`（唯一索引在叶子上，推断目标只能引用父表的约束，因此只能用无目标形式）。
- **重复投递跳过 Dify 调用**：这正是"持久化去重兜底"的目的，否则只防了重复行、
  没防重复付费回答。代价与现有 Redis 去重完全同构。
- **续期只建不删**：删数据不进服务的自动路径，留在 cron 脚本里显式调用。
- **不建 DEFAULT 分区**：能防插入失败，但一旦有行落进去，再建覆盖该区间的分区
  就必须 detach/搬数据/attach。自动续期 + 3 个月余量后，只有停机一个季度才会枯竭。
- **分区表从系统目录里发现，不写死表名**：这正是原脚本只写 `audit_logs`、
  `messages` 从此无人维护的那个洞。

## Step-by-Step Plan
- [x] 1. `001_core_schema.sql` 删掉那句永远建不起来的索引，注释说明去向
- [x] 2. `012_partition_maintenance.sql`：`ensure_month_partition` / `ensure_partitions` /
      `drop_month_partitions_before` / `ensure_message_dedup_index`；建议锁防并发；
      一次性补齐历史缺口并回填已有分区的去重索引
- [x] 3. `internal/state/partitions.go`：启动同步跑一次 + 每 24h ticker
- [x] 4. `cmd/router/main.go` 接线，`PARTITION_MONTHS_AHEAD` / `PARTITION_CHECK_INTERVAL`
- [x] 5. `CreateMessage` 返回 `(id, inserted, err)`，重复信号经 manager 传到 router 跳过模型调用
- [x] 6. 指标 `router_messages_duplicate_total` / `router_partitions_created_total` /
      `router_partition_maintenance_errors_total`
- [x] 7. `maintain_partitions.sql` 改为调用函数，audit_logs 90 天保留行为不变
- [x] 8. 验证（见下）

## 验证结果
- **全部 12 个迁移在全新库里 `ON_ERROR_STOP=1` 干净重放**——这是历史上第一次，
  此前 001 必然中断在最后一句。新库直接得到 messages/audit_logs 各 10 个分区。
- 活库复现的三个失败用例现在全部通过：当月 `messages` 写入、当月 `audit_logs` 写入、
  重复 `platform_msg_id` 被 `messages_2026_08_platform_msg_uniq` 拒绝。
- `ON CONFLICT DO NOTHING`（无推断目标）在分区父表上被接受并返回 0 行；
  `platform_msg_id IS NULL` 的出站消息可重复写入不受影响。
- `ensure_partitions` 第二次调用创建 0 个分区（幂等）。
- `drop_month_partitions_before('audit_logs', 90)` 删 3 个、再跑删 0 个；
  名字不合规范的分区跳过并 WARNING（它永远不会被清理，必须可见）。
- `go build ./... && go vet ./... && go test ./...` 全绿（13 个包）。
- 带库的集成测试走 `ROUTER_TEST_POSTGRES_URL`，不复用 `POSTGRES_URL`——
  这类测试会写行，不该因为 shell 里配了生产连接串就跑进生产库。无该变量时自动 skip。

## Current Status
- [x] Ready for Review

## 追加：`UpdateAppConfig` 端点修正（已完成）
`PUT /apps/{id}` 其实是应用**改名**接口，真实 Dify 0.15.3 返回
400 `Missing required parameter ... name`——补上 `name` 也不会改提示词。
正确接口是 `POST /apps/{id}/model-config`，且整体替换配置对象，
因此改成先读回当前对象、只替换提示词与变量声明再写回（配置存成 map 而非结构体，
否则每次写入都会把没建模的字段静默重置）。router 与 admin 两处同样的错误调用都修了。

顺带修掉两个"修了机制仍然没用"的洞：
- 旧 `DefaultSystemPrompt` 不引用任何变量，Go provision 出来的应用永远收不到本体事实；
  换成实测过的模板，并让 `UpdateAppConfig` 补齐 `user_input_form` 里的 6 个注入变量
  （Dify 会静默丢弃未声明的 input）。
- 提示词里的 `{{product_line}}` 渲染出来是 UUID——router 传的是产品线 ID。
  改为 provision 时静态替换产品线名（一应用一产品线，本来就是常量）。

验证：`go test ./internal/bridge/ -run LiveDify` 打真实 console，
先快照配置、结束恢复；黄金集重跑仍 60/60，标签率 92%，0 违规。

## 追加：enforce 熔断（已完成）
原计划第 2 期的三条代码任务（置信度重构 / `claim_conflict` 决策路径 / 按产品线灰度）
在第 1 期就已实现，剩下的全是要真实流量的验收。开 `enforce` 之前真正缺的是**上限**：
本体错一条断言，每一条碰到它的正确回答都会被压下转人工，规模随流量增长且无预警——
实测三条产品线里有两条第一次写就带这种缺陷，靠黄金集抓到，线上没有黄金集。

`internal/domain/breaker.go`：按产品线统计滑动窗口内被压下的比例，
超过 `trip_rate`（默认 25%，`min_samples` 20，`window` 100，`cooldown` 15min）
即自动停止拦截并告警。四条设计要点：

- **熔断态不是安全态**，是拿"发出可能有误的回答"换"人工队列不被打爆"，
  代价计入 `router_ontology_breaker_bypassed_total`，不藏。
- **shadow 期间窗口一样在统计**，所以比例已经离谱的产品线切到 enforce 时，
  第一条消息就被挡住，不会先压一批再发现。
- **冷却结束后按冷却期间的数据重新判断**，本体还是错的就继续不拦，
  不会"时间到了再拦一批"，也就不会来回抖动。
- **状态每进程独立，不走 Redis**：安全装置不该依赖一个出事时可能一起挂的组件。

`breaker` 块整块可省，默认开——它只放松客户主动开启的拦截，不收紧任何东西。
14 个单元测试钉住上述行为。并发测试写了，但 `-race` 在本机跑不了（无 gcc/cgo）。

## 未验证事项
本期及此前几期交付但**没有证据支持**的部分，全部登记在
[`doc/unverified.md`](unverified.md)：零真实流量、黄金集循环、
`router.go` 消息处理路径无测试覆盖（新接线只有依赖被测过）、
`cmd/evalset` 重实现判定链路且已开始与 router 漂移、熔断阈值是猜的、
`-race` 在本机跑不起来、数据库与 Dify 各只验过一个版本。
新增能力若当场验证不了，先往那个文件里登记再合并。

## 本期未做（与上一增量一致，仍在等外部条件）
- `validation: shadow` 跑真实流量，取覆盖率 / 误报率 / 标签发射率
- 用真实对话日志重写黄金集，本体由业务方编写（打破当前测试集的循环）
- `messages` 的保留策略尚未议定，`maintain_partitions.sql` 里留了位置没有启用
