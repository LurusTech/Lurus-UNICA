# judge：站在任何客服和客户之间的一道确定性闸门

它只做三件事：用业务自己写下的事实校验每一句回复、把没有依据的承诺扣下、把每次判定留成可校验的证据。不接渠道、不调模型、不带工单台。一个二进制：先体检，再接闸门。

```
go build -o judge ./cmd/judge

# L0 体检：喂历史对话，本机出一页报告，数据不出门
./judge checkup -in chatwoot.json -format chatwoot -packs common,ecommerce -out report.html

# L1 闸门：影子模式起服务，一个 HTTP 调用接入任何已有机器人或坐席工具
./judge serve -facts ./facts -packs common,ecommerce
curl -s localhost:8787/judge -H 'Content-Type: application/json' \
  -d '{"query":"坏了能赔吗","answer":"可以的，我们赔付200元，全额退款。","speaker":"human"}'
```

```json
{"mode":"shadow","decision":"send","withheld":false,"would_withhold":true,
 "would_reason":"commitment_unbacked",
 "would_detail":"答复含未经本体或知识库背书的承诺、金额或红线表述，已拦下待人工确认后再发：[unbacked_commitment] 「200元、全额」（原句：可以的，我们赔付200元，全额退款）",
 "evidence_id":"default/1","evidence_hash":"…"}
```

默认**影子模式**：全部判定、全部记录、一条不拦。`GET /shadow/<line>` 告诉你"若开启将扣下 N 条"——这个数是从证据链里数出来的，不是估的；样本不足 30 条时只给条数不给比例。切到 `-mode enforce`，同一套接法开始拦截。

## 四轴正交

| 轴 | 在哪 | 可替换？ |
|---|---|---|
| 内核：判定 | `pkg/judge`（纯函数）+ 本二进制的 HTTP 面 | 这是产品 |
| 内容：事实与行业包 | `facts/<店>.yaml` + `pkg/domain/redlinepacks/*.yaml` | 行业只在这里，代码不写行业 |
| 接入：适配器 | `adapters/dify-plugin`、`/adapters/chatwoot` | 薄层，随便换 |
| 运营：控制台与自演化 | UNICA 的 admin/router（不在本模块） | 建在证据之上，不反向耦合 |

路由器（`router/internal/routing`）、离线回放、体检报告和本二进制调用的是**同一个** `judge.Judge`。

## HTTP 面

| 方法 | 路径 | 作用 |
|---|---|---|
| `POST` | `/judge` | 判一条回复。见下 |
| `GET` / `PUT` | `/facts` · `/facts/{line}` | 读 / 整体替换某条产品线的事实 YAML（解析校验通过才生效，有 `-facts` 时原子写回文件；`/facts` 即默认线） |
| `GET` | `/evidence/{line}/{seq}` | 一条证据行 |
| `GET` | `/evidence/{line}` | 整链校验 + 链头摘要（可存到系统外做外部锚点） |
| `GET` | `/shadow` · `/shadow/{line}` | 影子汇总 |
| `GET` | `/healthz` | 存活、模式、事实是否已加载 |
| `POST` | `/adapters/chatwoot` | Chatwoot webhook（见下） |

设了 `-token`，除 `/healthz` 外全部要 `Authorization: Bearer <token>`；Chatwoot 回调不走这个头，未单独设 `-chatwoot-webhook-token` 时沿用 `-token` 的值。请求体上限 1 MB，超限返回 413 而不是截断。

**`POST /judge` 请求**

| 字段 | 说明 |
|---|---|
| `answer` | 待发回复，必填。模型的（带 `[FACT:]` 等标签）或人的（不带）都行 |
| `query` | 客户原话。用于关键词规则；客户自己说出的数字不算承诺的出处 |
| `grounding` | 检索段落原文数组。出现在里面的数字算有出处。只存摘要哈希进证据 |
| `retrieval_scores` | 检索相关度。**不传则不启用置信度门槛**——置信度是对检索管道的陈述，人的回复没有 |
| `product_line` | 选用哪条线的事实、写入哪条证据链、计入哪份影子汇总；默认 `default`。小写字母、数字与 `_ . -`，最长 64，不能以点结尾；因为它直接做文件名，所以不分大小写、拒绝系统保留名 |
| `conversation_id` · `correlation_id` | 写入证据行 |
| `speaker` | `ai` / `human`，只影响统计 |

**响应**：`decision`（send / handoff / send_and_handoff）、`withheld`（执法下扣下）、`would_withhold`（影子下"若开启将扣下"）、`reason`、`detail`、`answer`（剥完标签的客户可见文本；扣下时仍返回，给坐席改）、`claims` / `violations` / `commitments`、`evidence_id` / `evidence_hash`、`facts_version`。

闸门**从不改写回复，从不给出"正确值"**：一个找不到出处的数字，恰恰是系统不知道的数字。

## 事实（facts）

就是 UNICA 的本体 YAML（`doc/ontology-schema.md`）。`-facts` 给一个 `.yaml` 文件就是一家店（默认线）；给一个目录就是一家代运营机构：`<目录>/<店>.yaml` 一店一份，`product_line` 选用，`PUT /facts/<店>` 新建或替换，各店证据链与影子汇总互不相干。最小可用：

```yaml
product_line: Shop
properties:
  return_window_days:
    label: 无理由退货窗口
    range: {type: integer, unit: day}
assertions:
  - values:
      return_window_days: 7
```

没有事实也能跑：承诺防火墙与红线包不依赖本体。

## 证据链

每条判定是一行 `answer_audit` 同构的记录（`pkg/compliance`），按 `product_line` 成链，行哈希覆盖本行内容与上一行哈希。`-evidence <dir>` 时按行追加到 `<dir>/<line>.jsonl`，重启续链不归零；启动时整链校验，文件被改过会**拒绝启动**并指出第几条。不设 `-evidence` 则只在内存里，重启从 1 开始——试用可以，部署不行，启动日志会写明。

## 接入件

- **Dify**：`adapters/dify-plugin/README.md`。两种接法：HTTP 请求节点（不装任何东西）或插件骨架。回答后调 `/judge`，`withheld` 为真走转人工分支，否则发 `answer`。闸门不可达按转人工处理。
- **Chatwoot**：把 webhook 指到 `POST /adapters/chatwoot?token=<-chatwoot-webhook-token>`，订阅 `message_created`。每条外发消息（坐席或 agent bot）过闸、入链；有发现时以**私信备注**写回对话，客户看不到。Chatwoot 没有发送前钩子，所以这里对坐席是事后提示而非拦截——这正是人机同判的最小形态：AI 关掉，闸门照样有用。要拦截 bot 回复，用 Dify 接法在发送前判。
- **体检（L0，不接任何系统）**：`judge checkup`，喂历史对话导出（UNICA 导出 JSONL / Chatwoot API JSON / 带表头 CSV，`-csv-cols` 映射列名），本机出一页 HTML：无依据承诺清单、缺失事实清单（只说该写什么，不说写成多少）、红线命中、（给了 `-facts` 时）事实冲突与可自助比例。先脱敏（`pkg/redact`），残留个人信息就拒绝写报告；会话 ID 在报告里一律换成不可逆的短别名，因为导出里它常常就是手机号或昵称。一家机构可按店各出一份。

## 行业包

`pkg/domain/redlinepacks/<id>.yaml`，一包 = 红线词表 + 承诺类型（追加到防火墙线索词）+ 对抗用例（包自己的测试，体检里单列）+ 报告措辞。`ecommerce` 是样板。新增一个行业零代码改动；`-packs` 选用。

## 许可与边界

本模块与 `unica/pkg` 以 Apache-2.0 发布（见各自 `LICENSE`），且 `judge/go.mod` 只依赖 `pkg`——`GOWORK=off go build ./...` 在本目录可独立通过。UNICA 的自演化闭环、证据链校验服务、多租户控制台与官方渠道接入在其他模块，不在此许可下。

## 不做

不接通用 IM、不逆向协议、不自研检索、不训模型、不做语音；不单卖护栏或自演化；不在样本不足时给比例；不生成事实。
