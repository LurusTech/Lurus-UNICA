# 领域本体编写指南

本体是一份 YAML，声明"本业务哪些事实是确定的"，让 AI 回答时不再靠常识补全。
它与行业无关——电商、留学中介、财务代理、房产、同城服务用的是同一套语法。

工具（工作目录 `unica/router`）：

```bash
go run ./cmd/ontology validate -file 你的本体.yaml   # 检查语法与自洽性，不需要数据库
go run ./cmd/ontology preview  -file 你的本体.yaml   # 看它会给 AI 注入什么
POSTGRES_URL=... go run ./cmd/ontology publish -file 你的本体.yaml
POSTGRES_URL=... go run ./cmd/ontology versions -line 产品线名
POSTGRES_URL=... go run ./cmd/ontology rollback -line 产品线名 -version 2
```

`product_line` 必须与数据库 `product_lines.name` 一致。发布后旧版本保留，可随时回滚。

---

## 1. 本体装什么，不装什么

| 装 | 不装 |
|---|---|
| 政策、规则、价目、时效、资质（几十条，很少变） | 订单状态、客户进度、房源单价、库存（成千上万条，天天变） |
| 「退货 15 天，限未拆封」 | 「张三的订单到哪了」 |
| 「一般纳税人销售货物 13%」 | 「这家公司这个月申报了没」 |

**判据**：这条事实是不是对所有客户都一样？是 → 进本体。因人/因单而异 → 不进，
它应该走查询，系统已有的意图分诊会把「我的 XX 怎么样了」直接转人工。

本体的全部内容都会注入每次 AI 调用的上下文，所以它必须小。几十条属性是合适的量级。

---

## 2. 最小可用本体

```yaml
product_line: 明道财税     # 必须匹配 product_lines.name
version: 1

properties:
  月度申报截止日:
    label: 月度申报截止日        # 面向客户的措辞，会出现在注入的上下文里
    range: {type: integer, unit: 日, values: ["15"]}
    functional: true            # 单值：不能一会儿说 15 号一会儿说 20 号

assertions:
  - values:
      月度申报截止日: 15

denies:
  - term: 包过审
    note: 税务审核由税务机关决定，我们负责账务合规与申报及时
```

`denies` 往往是收益最高的部分——它把"我们没有这项服务"明确说出来。
不写的话，模型会用行业常识补一个听起来合理的答案。

---

## 3. 事实按情形不同时：作用域

绝大多数真实业务的事实不是无条件的。语法上有两种轴：

### 3.1 按商品/服务类别 → 用 `classes`

只有这根轴支持层级继承：父类上声明的事实，子类自动继承。

```yaml
classes:
  数码产品: {label: 数码产品}
  手机:     {label: 手机, subclass_of: 数码产品}
  配件:     {label: 配件, subclass_of: 数码产品}

disjoint:
  - [手机, 配件]        # 互斥：不能把手机按配件的规则回答

assertions:
  - class: 数码产品      # 两个子类共享
    values: {退货窗口: 15}
  - class: 手机
    values: {保修期: 12}
  - class: 配件
    values: {保修期: 6}
```

### 3.2 按其他情形 → 用 `dimensions`

服务阶段、客户身份、业务类型、地区——凡是"事实按某个轴分叉"的，都在这里声明。

```yaml
dimensions:
  服务阶段:
    label: 服务阶段
    values: [signed, submitted, delivered]
    labels:                       # 标识符给机器，标签给客户
      signed: 已签约未提交材料
      submitted: 已提交材料
      delivered: 已递交院校

assertions:
  - scope: {服务阶段: signed}
    values: {退费比例: 80}
  - scope: {服务阶段: submitted}
    values: {退费比例: 50}
  - scope: {服务阶段: delivered}
    values: {退费比例: 0}
```

### 3.3 两根轴交叉

`scope` 可以同时指定 class 和其他维度：

```yaml
assertions:
  - scope: {class: 一般纳税人, 业务类型: goods}
    values: {增值税率: 13}
  - scope: {class: 一般纳税人, 业务类型: services}
    values: {增值税率: 6}
  - class: 小规模纳税人                    # class: X 是 scope: {class: X} 的简写
    values: {增值税率: 3}
```

**为什么值得这么写**：一旦某个属性在不同情形下取值不同，AI 回答时不说清情形就会被判违规。
「增值税率是 3%」不说纳税人类型，「退费比例 80%」不说服务阶段——这些是要吃投诉的错误，
系统会自动拦下来。这是本体最值钱的一条检查。

---

## 4. 值类型

| type | 用于 | 示例 |
|---|---|---|
| `integer` | 天数、月数、期数 | `{type: integer, unit: day, values: ["15"]}` |
| `decimal` | 分数、系数 | `{type: decimal, min: 0, max: 9}` |
| `percent` | 比率、税率 | `{type: percent}` → 渲染成 `80%` |
| `money` | 金额 | `{type: money, unit: yuan}` → 渲染成 `12000元` |
| `date` | 截止日、生效日 | `{type: date}`，必须写 `YYYY-MM-DD` |
| `enum` | 固定说法 | `{type: enum, values: [顺丰, 到付]}` |
| `string` | 自由文本 | 不做校验，尽量少用 |

`unit` 支持 `day/workday/week/month/year/hour/minute/yuan/period/person/time`，
写别的会原样输出，所以中文单位（如 `所`、`日`）直接写也可以。

**把唯一正确答案钉死**：给 `values` 只列一个值，等于宣告"只有这个答案是对的"。
AI 说别的立刻违规。这是防跨产品线串答的主力手段。

```yaml
加急运费:
  range: {type: enum, values: [到付]}     # 别的产品线是 15 元 / 10 元，这里说数字就是错
```

---

## 5. 约束一览

| 写法 | 拦什么 |
|---|---|
| `domain: 类名` | 属性用错对象（对没有手机的业务谈手机保修） |
| `range.values` | 取值编错（退货说成 7 天而政策是 15 天） |
| `range.min/max` | 数值离谱（雅思填 11 分） |
| `functional: true` | 同一条回答自相矛盾（既说 12 个月又说 24 个月） |
| `min_cardinality: N` | 断言值给少了 |
| `requires: [属性B]` | **说 A 必须同时说 B**（说「15 天」不说「限未拆封」） |
| `disjoint: [[A, B]]` | 类互斥 |
| `denies` | 说了本业务根本没有的服务 |
| 多情形分叉（自动） | 不说明适用情形就给出唯一数字 |

`requires` 和多情形分叉容易混：

- **`requires`** 用于两个独立的事实必须同时出现。「退货 15 天」和「限未拆封」是两条事实。
- **多情形分叉** 用于同一个事实按情形取不同值。「退费比例」只有一条，值随阶段变。

用错会导致循环（拿属性去重述作用域），`validate` 会报出来。

---

## 6. 两个开关

写在 `product_lines.config_json` 里，与 `guardrail` 并列：

```json
{
  "ontology": {
    "inject_facts": true,
    "validation": "shadow"
  }
}
```

| 开关 | 取值 | 含义 |
|---|---|---|
| `inject_facts` | `true` / `false` | 把确定性事实注入 AI 上下文 |
| `validation` | `off` / `shadow` / `enforce` | 对 AI 的断言做校验 |

两个开关互相独立，因为不同客户要的不一样：

- 只开 `inject_facts` —— 给 AI 喂准事实，但从不因此拦回答。**推荐的第一步。**
- `inject_facts` + `validation: shadow` —— 加上校验观察，仍不改变任何判定。
- `enforce` —— 违规即拦截并转人工，回答被压下，坐席收到违规说明。

> ⚠️ **不要只开 `validation` 不开 `inject_facts`。**
> 断言标签的词表是随事实块一起注入的，不注入事实，模型就没有可抄的标签。
> 实测：注入时模型在 88% 的回答上带标签，不注入时只有 2%。
> 单开校验会让精确检查基本空转，只剩文本级的 `denies` 扫描在跑，
> 你会看到一片"没有违规"，那不是没问题，是没在查。

**默认两个都关**，升级到新版本不会改变任何现有行为。

### 阈值与置信度档位

开启 `inject_facts` 后，回答的置信度不再只看检索命中——注入的事实不产生检索结果，
若只按检索打分，本体越是让模型答对，护栏越是把答案拦下来。现在按证据强度分档：

| 档位 | 何时取到 |
|---|---|
| 0.90 | 注入了事实，且回答的断言经校验全部成立 |
| 0.75 | 注入了事实，回答未与之冲突（但没做出可校验的断言） |
| 0.72 | 只召回了历史经验（启发式证据，弱于确定性事实） |
| 检索均值 | 未注入任何上下文时的原有行为，一分不变 |
| 0 | 回答与本体冲突 |

于是 `confidence_threshold` 一个参数就能表达风险偏好：

```
0.70（默认）  检索命中 / 经验召回 / 确定性事实都接受
0.75          必须有确定性事实，经验召回不算数
0.80          必须有事实 + 校验通过的断言
```

上线建议：`shadow` 跑一到两周，看 `claim_violations` 表里的判定准不准（有没有误杀正确答案），
再开 `enforce`。部署级总开关是 `ONTOLOGY_ENABLED=false`，用于出事时一键全关。

---

## 7. 提示词要改一行

要让校验的精确那一半生效，需要 AI 在回答里带上断言标签。在 AI 应用的提示词里加：

```
参考以下确定性事实回答，冲突时以事实为准：
{{facts_context}}

回答中每引用一条上述事实，在句末附加标签 [FACT:属性名=取值]；
若该事实按情形不同，写成 [FACT:情形.属性名=取值]。标签不会展示给客户。
```

**不加也能用**：没有标签时，`denies` 的文本扫描仍然生效，能拦下"说了不存在的服务"这类错误。
标签只是让检查更精确。

提示词里**不要写具体数字**，只写 `{{facts_context}}` 引用——这样改政策只改本体，
永远不用回控制台改提示词。

---

## 8. 常见错误

| 现象 | 原因 |
|---|---|
| `product line X not found` | `product_line` 与数据库 `product_lines.name` 不一致 |
| `value "X" appears in both A and B` | 两个维度用了同名取值；作用域按值解析，必须全局唯一 |
| `property X requires itself` | `requires` 指向自己 |
| `assertion N sets undeclared property` | 断言里的属性没在 `properties` 声明 |
| 注入内容里出现英文标识符 | 枚举/维度没配 `labels`，补上中文措辞 |
| 正确答案被判违规 | 多半是 `values` 钉得过死，或该属性其实分情形而本体没分 |
