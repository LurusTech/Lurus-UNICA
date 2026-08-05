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

## 本期未做（与上一增量一致，仍在等外部条件）
- `dify_admin.go` 的 `UpdateAppConfig` 端点修正（照 `deploy/dify-preview/configure_apps.py`）
- `validation: shadow` 跑真实流量，取覆盖率 / 误报率 / 标签发射率
- 用真实对话日志重写黄金集，本体由业务方编写（打破当前测试集的循环）
- `messages` 的保留策略尚未议定，`maintain_partitions.sql` 里留了位置没有启用
