# Dify 接入：回答后过闸

两种接法，效果相同。第一种不装任何东西，十分钟内可接通；第二种是插件骨架，装好后在节点面板里直接选"过闸"。

## 接法一：HTTP 请求节点（推荐先用这个）

在现有 Chatflow / Workflow 的 **LLM 节点之后、回复节点之前**插入两个节点：

**1. HTTP 请求节点 `judge`**

| 项 | 值 |
|---|---|
| 方法 / URL | `POST http://<judge 地址>/judge` |
| Header | `Content-Type: application/json`；设了 `-token` 时再加 `Authorization: Bearer <token>` |
| Body（JSON） | 见下 |

```json
{
  "query": "{{#sys.query#}}",
  "answer": "{{#llm.text#}}",
  "grounding": {{#knowledge-retrieval.result#}}  ,
  "product_line": "<产品线或店铺名>",
  "conversation_id": "{{#sys.conversation_id#}}"
}
```

`grounding` 要的是检索段落**原文**数组。知识检索节点的 `result` 是对象数组，直接塞进去不行；在中间加一个代码节点把 `content` 字段取出来：

```python
def main(result: list) -> dict:
    return {"texts": [r.get("content", "") for r in (result or []) if r.get("content")]}
```

然后 `"grounding": {{#code.texts#}}`。不传 `grounding` 也能跑，只是检索到的数字会被当作无出处。

**2. 条件分支节点**

- `{{#judge.body.withheld#}}` 为 `true` → 走转人工分支（把 `{{#judge.body.detail#}}` 和 `{{#judge.body.answer#}}` 作为坐席草稿一起带过去）
- 否则 → 回复节点输出 `{{#judge.body.answer#}}`（**用这个，不要用 `llm.text`**：闸门已剥掉 `[FACT:]`/`[INTENT:]`/`[HANDOFF:]` 标签）

影子模式下 `withheld` 永远是 `false`，分支不会触发；`would_withhold` 记在留痕链里，`GET /shadow/<product_line>` 看汇总。切到 `-mode enforce` 后，同一套节点自动开始拦截。

**失败即转人工**：HTTP 节点请把"异常处理"设为走转人工分支。闸门不可达不能等于放行。

## 接法二：插件骨架

本目录是一个 Dify 工具插件的骨架（`manifest.yaml`、`provider/`、`tools/`）。工具 `judge` 做的事与接法一完全一样，只是把地址与令牌收进凭据，并在闸门不可达时**返回 `withheld=true`**（失败即转人工），不用再配异常分支。

打包与安装步骤以 Dify 官方插件开发文档为准（需要官方的插件打包工具）。**本骨架尚未在真实 Dify 实例上打包安装验证**，见 `doc/unverified.md`。

## 行为边界

- 闸门不改写回复。扣下时返回原文与原因，怎么处理由工作流决定。
- 闸门不生成事实。`detail` 里写的是"找不到出处"，不是"正确值应为"。
- 两种接法都只在回答后判一次；LLM 节点之前的分诊（关键词转人工）仍由 Dify 侧或 UNICA 路由器负责。
