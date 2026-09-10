# Active Task: 无

当前没有进行中的增量。

**配置面重构阶段 4 已全部完成**（A/B/C/D 四组），方案与逐条验收记录见
`doc/plan-workbench-settings.md`。各增量的实现过程、被拦下的问题与实机验证结果
记在各自的提交信息里：

| 组 | 提交 | 内容 |
|---|---|---|
| A | `acd35c3` | 模型配置收回门户，落库可写、先验证后提交 |
| D | `6681493` | 401 自动续期、按账号索引的「记住我」、D22 结案 |
| C3/C4 | `6dd7d67` | 文档分段只读接口与门户查看 |
| C2 | `a7e817f` | Dify 控制台免密直达（仅 admin） |
| B | `a263c97` | 运行开关落库热生效、索引方式可写、未启用能力清单、设置页四区重排 |

开新增量时按 `CLAUDE.md` 第 10 节的格式覆盖本文件。

## 挂着的账（与阶段 4 无关，各自独立）

都记在 `doc/known-defects.md` 里，这里只留索引：

- **D24** 三条演练线的知识库索引 `economy` 配检索 `semantic_search`，检索恒空且不报错。
  存量数据问题，修法在数据侧（改检索设置或重建索引），不是改代码。
- **D25** `pkg/domain` 的种子本体与 `canned_responses.yaml` 漂移，
  `TestSeedOntologiesMatchCannedResponses` 常红。要先定以哪份为准。
- **D23** refresh token 能当 Bearer 通过 `AuthMiddleware`（低危，无数据泄露，未修）。
