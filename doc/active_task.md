# Active Task: AI 设置页收拢 S1–S4（纯接线，不动界面）

## Context
`doc/plan-ai-settings-consolidation.md` 的前三步。三步全是**接线**：让配置真的可读、真的能校验、
真的立即生效，之后（S5 起）才往界面上放。分开做是这次的纪律——先证明它生效，再暴露它。
本增量**不改任何门户页面**，也不新增任何租户可写的设置。

## 为什么是这三步先
- **S1**：admin 的 Config 里**一个 router 的 env 都没有**。任何"展示平台总闸档位"的功能，
  照直觉去读 admin 自己的环境变量，会自信地显示相反的值。
- **S2**：检索设置的修复入口已经存在，但写入前不校验索引技术是否匹配——把 economy 索引的库
  改成语义检索，检索会恒空，而现有的回读校验只比对 `search_method`，发现不了。
- **S3**：ontology 的配置写入**不失效路由缓存**，关掉 enforce 最长 5 分钟不生效；
  更糟的是顺手保存一次 guardrail 就会冲掉缓存，导致该缺陷**间歇性消失**——
  在 S3 之前，任何现场复现都不可信。

## Critical Files
- `unica/router/cmd/router/main.go`（新增 /configz）
- `unica/admin/internal/config/config.go`（router 内网地址）
- `unica/admin/internal/bridge/dify.go`（数据集读写补全）
- `unica/admin/internal/routecache/`（新建：缓存失效的唯一实现）
- `unica/admin/internal/tenant/aisettings/aisettings.go`（改用共享实现，回报失效结果）
- `unica/admin/internal/tenant/facts/facts.go`（接上缓存失效）

## Step-by-Step Plan
- [x] 1. router 新增 `GET /configz`：返回运行时**实际生效**的行为开关
      （triage / scene / ontology 开关与缓存 TTL / route cache TTL / idle timeout / ACEST 是否启用）。
      **只出开关，不出任何凭据与连接串**——这个端点和 /healthz、/metrics 一样无鉴权。
- [x] 2. admin 侧新增 router 内网地址配置与取数客户端（短缓存），
      并暴露 `GET /api/v1/platform/runtime`（仅 admin）。
      完成标志：admin 读到的值与 router 容器里的 env 一致；改 env 重启 router 后 admin 读到新值。
- [x] 3. bridge 抽出"读数据集当前 retrieval_model + indexing_technique"的方法。
- [x] 4. `SetDatasetRetrieval` 写入前比对索引技术：不匹配则**拒写**并报"请先重建索引"，
      而不是写进去让检索恒空。
- [x] 5. 回读校验从只校 `search_method` 扩到 `top_k`（以及可得的 rerank 开关）。
      现在这条回读是唯一挡住"200 但没生效"的东西，覆盖面太窄。
- [x] 6. `want` 改成「平台默认 + 租户覆盖合并」的构造形态（当前租户覆盖恒为空）。
      现在就定好形状，否则 S11 加 top_k 时两个写入口会在同一张卡上互相静默回滚。
- [x] 7. 新建 `internal/routecache`，把失效逻辑抽成唯一实现；aisettings 改用它。
- [x] 8. facts 的 `putConfig` 接上失效（需改构造函数与 main 接线）。
- [x] 9. 失效失败不再静默 return：记 WARN，并在响应里返回 `cache_invalidated` 布尔。
      门户当前无条件写着"立即生效"，这个字段是后续让它说实话的依据。
- [x] 10. 验证：五模块 build+vet+test；`/configz` 与实机 env 逐项对拍；
      构造一次索引技术不匹配，确认**拒写**而非打印成功；
      改 ontology 配置后秒级观测到 router 行为变化。

## Current Status
- [x] S1–S4 完成（2026-08-24）

### S4 —— 应用变量声明对拍与补齐

`pkg/difyapp` 新增 `DeclaredVariables` / `MissingContextVariables`；
bridge 的 `GetAppConfig` 把对拍结果一并带出（`AppInfo.Variables`），
并新增幂等的 `EnsureContextVariables`；
新接口 `POST /ai-settings/variables/repair`（仅 admin，与 `dataset/bind` 同类：
修的是被开出来的应用，不是租户的偏好）。

**为什么这个检查有价值**：Dify 会把应用未声明的输入**静默丢弃**——不报错、
答复里也看不出来。于是"本体没话说"和"本体压根没送到"这两件事，从答复上完全无法区分。
对拍是唯一能把它们分开的地方。

**构造性验收**（`tmp/verify_s4.py`）：五条线目前全是齐的，被动检查证明不了任何事，
所以人为造了一个缺失：

```
1. 开一次性租户 VARTEST
2. 开户后 complete=True                     ← 开户流程本身没问题
3. 从 Dify 里删掉 scene_context             ← 模拟手工改过/早期开户的应用
4. 门户报 complete=False missing=[scene_context]
5. 修复 added=[scene_context]
6. 复检 complete=True，declared=7 项
7. 再修一次 already_complete=True, added=None   ← 幂等
8. 销户，Dify 应用与 Chatwoot 账户随之清理
```

另有单测四条（`pkg/difyapp/prompt_variables_test.go`）覆盖：漏报、全齐、
空表单（全缺）、以及**修复必须是增量的**——运维自己加的变量不能被冲掉、重复执行不产生改动。

五条产线经新接口复查：全部 `complete=True`。

### S4 的定位（与调研报告的判断不同）

调研称这是"本次最大漏项"。**核减**：五条线的七个变量一直是齐的，
它是**预防性**体检，不是在修一个正在发生的故障。
相应地，报告称 S4 会让"一批租户第一次真的收到 facts_context"从而金标必然漂移——
对这五条线不成立，本次金标零影响。

### 实机验收

```
router /configz  {"intent_triage":"shadow","scene_mode":"on","ontology_enabled":true,
                  "ontology_cache_ttl":"5m0s","route_cache_ttl":"5m0s",
                  "idle_timeout":"30m0s","acest_enabled":false,"workers":4}
router.env       只有 SCENE_MODE=on —— 其余全部落默认值，与上面逐项对得上
admin 转述       available:true，八个字段与 router 完全一致
租户 user 访问   403
facts/config 写入   -> "cache_invalidated": true
ai-settings 写入    -> "cache_invalidated": true
```

### 顺带纠正一处我自己写错的事实

我此前说"当前 `INTENT_TRIAGE` 未设置，落到 **off**"——**错了，落到的是 shadow**
（`triage.go:36` `DefaultTriageMode = TriageShadow`）。结论不变：
`EvaluateWithMode` 只在 `TriageOn` 下弃用关键词表，shadow 下关键词照常生效。
`doc/plan-ai-settings-consolidation.md` 第 0.2 节已改。
这正好说明 S1 存在的理由——**这个值没有任何界面能看到，只能靠猜，而我就猜错了。**

### 与计划的差异

- **数据集覆盖字段的合并形态提前做了**（S2 第 6 步）：`DatasetConfig.Overrides`
  当前恒为空，但 PATCH 替换的是整个 `retrieval_model` 对象，等 S11 加 top_k 时
  才引入合并就晚了——两个写入口会在同一张卡上互相静默回滚。
- **`routeCacheKeyPrefix` 在测试里刻意重写了一份**而不是 import 共享常量：
  这个测试要证明的是"router 会读的那个 key 没了"，跟着被测代码改名会让测试
  在 key 改错时照样绿。

### 遗留

- 门户仍无条件显示"立即生效"，没有读 `cache_invalidated`。这是 S5 之后的事，
  但接口已经在说实话了。
- `/configz` 无鉴权（与 `/healthz`、`/metrics` 一致），因此只出开关、不出任何
  连接串与凭据；ACEST 只报 enabled 真假。

## 不在本增量内
门户任何改动、体检卡、提示词契约校验、调研文案、top_k 可写、admin.html 平台只读卡。
它们是 S4 之后的事，见 `doc/plan-ai-settings-consolidation.md`。

## 既存红灯
`pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses` 先于阶段 0 就红，根治属阶段 3（D8）。
