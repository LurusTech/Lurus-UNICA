# 开源竞品存活普查与 UNICA 借鉴报告（截至 2026-10-09，核查修订版）

> 数据口径：星数、最近提交、90 天提交都取自普查当日的 GitHub 实时值。90 天提交在接口单页上限处封顶，所以写作"≥100"，实际可能更多。**普查没有 6 月的星数基线，"6 月以来增长了多少"算不出来**，"受欢迎"只能用"6 月前已知、现在星数高、仍然活跃"来近似。各项目案例里的数字大多是厂商自己说的，引用时都标了出处。
> 修订说明：第 4 节每条后面加了【仓库现状】，依据是对 `C:\Users\Anita\Desktop\lurus\2b-svc-unica` 的只读核查，包括未提交的工作区改动。证据较弱的论断已经复核，修正之处见文末"核查记录"。

---

## 1. 结论摘要

1. **"可自证、不乱承诺"这一整套，开源世界里还没有对手。** 确定性事实注入并校验、承诺防火墙、按租户哈希链留痕、统计验证门驱动的自演化，再加上抖音、淘宝、快手、小红书的官方客服接口，没有任何一个存活项目同时具备。理念最接近的 Parlant（https://github.com/emcie-co/parlant）默认分支最后一次提交是 2026-07-10，90 天内 0 提交，处于放缓状态（已复核）。
2. **最大的威胁不是哪个产品，而是"拼装方案"。** 集成商用 Dify + LangBot + Chatwoot 就能低价搭出 UNICA 的骨架（https://github.com/langbot-app/LangBot ，约 1.8 万星，Apache-2.0）。另一边，Chatwoot 的 Captain 已经有 guidelines、guardrails、playground，正在靠近"可控 AI 客服"的说法。所以护城河要压在确定性校验、证据链和电商官方渠道上。通用 IM 渠道和检索质量都不算护城河。
3. **6 月前已经火、现在还活着的项目，几乎都有商业母体或者被收购。** 例如 CowAgent 背后是 LinkAI，Langfuse 被 ClickHouse 收购，promptfoo 被头部模型厂商收购（2026-03 宣布），Guardrails AI 被 Harvey 收购（2026-09-09）。只做"接个大模型回消息"或"通用护栏"的项目在变慢或停更，包括 XianyuAutoAgent、dify-on-wechat、wechaty。Flowise 是官方有计划地收尾：先冻结代码，再归档，最后终止维护。Guardrails AI 的活跃度也在下滑。
4. **最值得先借的是"少转人工、但不放松确定性终裁"这类机制。** 校验失败先回灌重写一次（CrewAI、Guardrails AI）；高风险意图用"预审模板加本体槽位"作答（Parlant）；从每条本体约束自动派生对抗用例（promptfoo）；给事实加双时间字段（Graphiti）。这几条都直接作用在北极星的自助解决率和事实新鲜度上，零真实流量时也能做。核查确认这四条在仓库里**都还没有，或只有一部分**。另外，清单里有几条（运营日报、决策证据字段、策略包）**仓库已经基本实现**，不应再占排期，见第 4 节。
5. **落地证据最强的场景是"高赔付风险、事实能结构化"的对外客服**，例如支付和小商户、银行、电信、电商售后。放到 UNICA 的中国中小企业私有化定位上，首选**电商品牌店售后承诺管控**。售前用"历史对话体检报告"切入，这也能顺带缓解没有真实流量的问题。

---

## 2. 存活全景表

状态判定：60 天内有提交为 alive，60 到 180 天为 slowing，180 天以上或已归档为 dead。项目名后带 ※ 的，表示 6 月之后才进入视野。

### 2.1 客服台、对话平台

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| Rocket.Chat https://github.com/RocketChat/Rocket.Chat | 46240 | 2026-10-10 | ≥100 | alive | 协作和 Contact Center，政务私有化强，AI 不是核心 |
| Chatwoot https://github.com/chatwoot/chatwoot | 37663 | 2026-10-09 | ≥100 | alive | UNICA 的坐席层；Captain 正在补可控 AI |
| Parlant https://github.com/emcie-co/parlant | 18298 | 2026-07-10 | 0 | slowing | guideline、canned response、可追溯，理念最接近 |
| Botpress https://github.com/botpress/botpress | 14947 | 2026-10-09 | ≥100 | alive | 以云服务为主；定义了"AI 解决率"的说法和定价锚点 |
| Typebot https://github.com/baptisteArno/typebot.io | 10520 | 2026-10-07 | 67 | alive | 可视化对话流，fair-source 许可 |
| Zammad https://github.com/Zammad/zammad | 5993 | 2026-10-09 | ≥100 | alive | 传统工单台 |
| FreeScout https://github.com/freescout-help-desk/freescout | 4592 | 2026-10-09 | ≥100 | alive | 已改口称自己是 AI-powered helpdesk |
| erxes https://github.com/erxes/erxes | 4094 | 2026-10-09 | ≥100 | alive | 营销、销售、客服一体的 XOS |
| LibreDesk※ https://github.com/abhinavxd/libredesk | 3015 | 2026-10-09 | ≥100 | alive | 单二进制自托管，小号 Chatwoot |
| 春松客服 https://github.com/cskefu/cskefu | 2901 | 2026-09-23 | 6 | alive | 中文开源客服，节奏很慢，最近一次 release 在 2023 年 |
| Hexabot※ https://github.com/hexabot-ai/hexabot | 1149 | 2026-08-20 | ≥100 | alive | 转向"AI 工作流自动化"，v3 还没发 release |
| Tiledesk https://github.com/Tiledesk/tiledesk | 322 | 2026-09-14 | 8 | alive | LLM 机器人加人工转接，场景接近但体量小 |
| agent-desk※ https://github.com/huabeitech/agent-desk | 266 | 2026-10-07 | ≥100 | alive | AI-first 客服，方向接近，维护很快 |
| Dify https://github.com/langgenius/dify | 158021 | 2026-10-10 | ≥100 | alive | UNICA 的生成引擎；许可证对多租户有约束 |
| Appsmith https://github.com/appsmithorg/appsmith | 41043 | 2026-10-09 | ≥100 | alive | 低代码内部工具 |
| ToolJet※ https://github.com/ToolJet/ToolJet | 41055 | 2026-10-09 | 1817 | alive | 低代码，营销上开始讲 Auto-Support |

### 2.2 LLM 应用和编排平台

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| OpenClaw https://github.com/openclaw/openclaw | 391535 | 2026-10-10 | ≥100 | alive | 个人和团队的 agent 网关，33 个渠道；星数爆发，是否稳定待观察 |
| n8n https://github.com/n8n-io/n8n | 206843 | 2026-10-09 | 3784 | alive | 自动化"胶水"，有量化的客服工单案例 |
| Langflow https://github.com/langflow-ai/langflow | 155444 | 2026-10-10 | 828 | alive | 可视化编排，偏开发者原型 |
| Open WebUI https://github.com/open-webui/open-webui | 154155 | 2026-10-09 | ≥100 | alive | 企业内部聊天界面，白标要收费 |
| RAGFlow https://github.com/infiniflow/ragflow | 91921 | 2026-10-09 | 2624 | alive | 深度文档解析和可追溯引用 |
| LobeHub https://github.com/lobehub/lobehub | 83088 | 2026-10-10 | ≥100 | alive | 转型为 Agent 运营 |
| AnythingLLM https://github.com/Mintplex-Labs/anything-llm | 66870 | 2026-10-09 | 302 | alive | 本地优先全家桶，带网站挂件 |
| CrewAI https://github.com/crewAIInc/crewAI | 59510 | 2026-10-09 | ≥100 | alive | 多 Agent 框架；任务护栏失败后回灌重写 |
| FastGPT https://github.com/labring/FastGPT | 29789 | 2026-10-09 | 439 | alive | 国产知识库编排，许可证要人工确认 |
| MaxKB https://github.com/1Panel-dev/MaxKB | 22929 | 2026-10-09 | 197 | alive | 国产知识库问答，GPL-3.0 |
| BISHENG https://github.com/dataelement/bisheng | 12029 | 2026-10-09 | ≥100 | alive | 国产企业级 LLM 应用平台 |
| Coze Loop https://github.com/coze-dev/coze-loop | 5764 | 2026-10-09 | 68 | alive | agent 调试和评测 |
| Bytedesk※ https://github.com/Bytedesk/bytedesk | 504 | 2026-10-07 | 54 | alive | 开源 IM 加 AI 客服，全渠道，AGPL |
| Customer-Agent※ https://github.com/JC0v0/Customer-Agent | 931 | 2026-09-22 | 26 | alive | 电商客服，未声明许可证 |

### 2.3 中国渠道和 IM 接入

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| CowAgent https://github.com/zhayujie/CowAgent | 47303 | 2026-10-09 | ≥100 | alive | 原来的微信机器人，转成 Agent Harness，靠 LinkAI 变现 |
| AstrBot https://github.com/AstrBotDevs/AstrBot | 41621 | 2026-10-09 | ≥100 | alive | 多 IM 的 Agent 框架，插件生态，AGPL |
| WeKnora https://github.com/Tencent/WeKnora | 32847 | 2026-10-09 | ≥100 | alive | 微信对话开放平台的底座，带 Lite 单二进制 |
| LangBot https://github.com/langbot-app/LangBot | 18072 | 2026-10-09 | ≥100 | alive | "任意 Agent 接任意 IM"的网关，对 UNICA 替代性最强 |
| PandaWiki https://github.com/chaitin/PandaWiki | 10309 | 2026-10-09 | 25 | alive | 知识库、Wiki、问答机器人，有具名客户墙 |
| xianyu-auto-reply https://github.com/zhinianboke/xianyu-auto-reply | 7501 | 2026-10-09 | ≥100 | alive | 闲鱼回复加自动发货，走逆向协议 |
| NoneBot2 https://github.com/nonebot/nonebot2 | 7738 | 2026-10-09 | 95 | alive | 适配器和框架解耦，所以扛得住平台接口变化 |
| Koishi https://github.com/koishijs/koishi | 6247 | 2026-10-08 | 3 | alive | 接近停滞 |
| dsh-im※ https://github.com/xmanrui/dsh-im | 1755 | 2026-10-08 | ≥100 | alive | 9 个渠道的 IM 接入；版本号和仓库年龄对不上，需要核对 |

### 2.4 管控、护栏、红队

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| Langfuse https://github.com/langfuse/langfuse | 35580 | 2026-10-09 | ≥100 | alive | 观测和评测，有客服案例（厂商自述） |
| promptfoo https://github.com/promptfoo/promptfoo | 25850 | 2026-10-10 | ≥100 | alive | 红队测试，带 contracts 和 ecommerce 插件 |
| DeepEval https://github.com/confident-ai/deepeval | 18725 | 2026-10-10 | ≥100 | alive | 用写单测的方式评测，有多轮指标 |
| garak https://github.com/NVIDIA/garak | 9513 | 2026-10-09 | ≥100 | alive | LLM 漏洞扫描器 |
| Guardrails AI https://github.com/guardrails-ai/guardrails | 7503 | 2026-10-09 | 32 | alive | validator 加 on_fail 动作；2026-09 被 Harvey 收购，开源库保留 Apache-2.0 并仍在更新 |
| NeMo Guardrails https://github.com/NVIDIA-NeMo/Guardrails | 7270 | 2026-10-09 | ≥100 | alive | 五类 rail，有电信、零售落地 |
| Agent Governance Toolkit※ https://github.com/microsoft/agent-governance-toolkit | 6415 | 2026-10-09 | ≥100 | alive | 策略执行、Merkle 审计、合规映射 |
| Giskard https://github.com/Giskard-AI/giskard-oss | 5882 | 2026-10-09 | ≥100 | alive | 多轮红队，有银行客服案例 |
| PyRIT https://github.com/microsoft/PyRIT | 4600 | 2026-10-10 | ≥100 | alive | 生成式 AI 风险识别 |
| Wanwu https://github.com/UnicomAI/wanwu | 2362 | 2026-09-25 | ≥100 | alive | 联通的多租户智能体平台 |
| tau2-bench https://github.com/sierra-research/tau2-bench | 2198 | 2026-10-07 | 58 | alive | 客服 agent 遵守政策的基准 |
| Cedar https://github.com/cedar-policy/cedar | 1775 | 2026-10-09 | 84 | alive | 策略语言 |
| agent-control※ https://github.com/agentcontrol/agent-control | 324 | 2026-10-09 | 34 | alive | 运行时 agent 治理的控制面 |
| OpenGuardrails※ https://github.com/openguardrails/openguardrails | 53 | 2026-09-25 | ≥100 | alive | 开放协议加基准，社区很小 |

### 2.5 评测、观测、自优化

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| DSPy https://github.com/stanfordnlp/dspy | 38571 | 2026-10-09 | ≥100 | alive | 提示词和裁判优化；Nubank 用在客服裁判上 |
| MLflow https://github.com/mlflow/mlflow | 28331 | 2026-10-09 | ≥100 | alive | 裁判注册和对齐，三档解决度 |
| Opik https://github.com/comet-ml/opik | 22475 | 2026-10-09 | ≥100 | alive | Apache-2.0 可完整自托管，Dify 原生集成 |
| Agent Lightning https://github.com/microsoft/agent-lightning | 18630 | 2026-10-09 | 60 | alive | agent 强化学习，需要 GPU |
| SkillOpt※ https://github.com/microsoft/SkillOpt | 18169 | 2026-10-06 | ≥100 | alive | 验证门控的文本自演化，和 UNICA 的闭环同构 |
| Arize Phoenix https://github.com/Arize-ai/phoenix | 11769 | 2026-10-10 | ≥100 | alive | 会话级评测；ELv2 许可 |
| OpenPipe ART https://github.com/openpipe/art | 10792 | 2026-10-10 | ≥100 | alive | 相对排序做奖励 |
| OpenLLMetry https://github.com/traceloop/openllmetry | 7480 | 2026-10-06 | 17 | alive | OTel GenAI 埋点，被收购后变慢 |
| GEPA https://github.com/gepa-ai/gepa | 6923 | 2026-10-09 | 77 | alive | 文本优化器 |
| Helicone https://github.com/Helicone/helicone | 6209 | 2026-09-16 | 10 | alive | 观测平台，活跃度偏低 |
| LangWatch※ https://github.com/langwatch/langwatch | 4928 | 2026-10-09 | ≥100 | alive | 企业 agent 观测和评测 |
| Latitude※ https://github.com/latitude-dev/latitude-llm | 4716 | 2026-10-09 | ≥100 | alive | 转向 agent 失败定位 |
| Laminar※ https://github.com/lmnr-ai/lmnr | 3368 | 2026-10-09 | ≥100 | alive | agent 观测 |
| Inspect AI https://github.com/UKGovernmentBEIS/inspect_ai | 2966 | 2026-10-10 | ≥100 | alive | 评测框架 |
| SkillEvaluator※ https://github.com/NVIDIA/SkillEvaluator | 554 | 2026-10-09 | ≥100 | alive | skill 质量门 |

### 2.6 知识、记忆、语音

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 一句话 |
|---|---|---|---|---|---|
| Graphify※ https://github.com/Graphify-Labs/graphify | 125043 | 2026-10-10 | ≥100 | alive | 确定性建图，边带来源标注 |
| Understand-Anything※ https://github.com/Egonex-AI/Understand-Anything | 85760 | 2026-10-09 | ≥100 | alive | 图谱导览和变更影响分析 |
| Mem0 https://github.com/mem0ai/mem0 | 66909 | 2026-10-09 | ≥100 | alive | 记忆层，官方把客服列为首要用例 |
| LightRAG https://github.com/HKUDS/LightRAG | 40039 | 2026-10-08 | ≥100 | alive | 国内私有化知识库常用底座 |
| GraphRAG https://github.com/microsoft/graphrag | 36277 | 2026-10-08 | 26 | alive | 官方声明进入维护模式 |
| cognee https://github.com/topoteretes/cognee | 31910 | 2026-10-09 | ≥100 | alive | 记忆平台，主打本体加溯源 |
| Graphiti https://github.com/getzep/graphiti | 31600 | 2026-10-09 | ≥100 | alive | 双时间轴、溯源、失效但不删除 |
| Letta https://github.com/letta-ai/letta | 25092 | 2026-09-10 | 7 | alive | 明显降速，5 个月没有新版本 |
| RAG-Anything https://github.com/HKUDS/RAG-Anything | 23506 | 2026-10-06 | 58 | alive | 多模态文档 RAG |
| FunASR https://github.com/modelscope/FunASR | 20623 | 2026-10-08 | ≥100 | alive | 语音识别 |
| Pipecat https://github.com/pipecat-ai/pipecat | 16313 | 2026-10-10 | ≥100 | alive | 语音 agent 框架 |
| LiveKit Agents https://github.com/livekit/agents | 14668 | 2026-10-10 | ≥100 | alive | 实时语音 agent |
| TEN Framework https://github.com/TEN-framework/ten-framework | 11155 | 2026-10-09 | 74 | alive | 实时多模态对话 |
| Zep https://github.com/getzep/zep | 4953 | 2026-10-08 | 34 | alive | 服务端，存在感不如 Graphiti |

### 2.7 6 月已受关注、现已停更或放缓的项目

| 项目 | 星数 | 最近提交 | 90 天提交 | 状态 | 可能原因（只写有证据的） |
|---|---|---|---|---|---|
| Flowise https://github.com/FlowiseAI/Flowise | 55496 | 2026-08-13 推送 | 23 | dead（已归档） | **官方计划内收尾**（Discussion #6727 "The Future of Flowise"）：2026-07-29 冻结代码、不再接 PR，2026-08-13 归档并把 npm 包和 Docker 镜像标为弃用，2026-08-31 核心团队退出。最后一个 release 是 3.1.4。代码按 Apache-2.0 鼓励 fork；自托管仍能跑，但之后不再有安全补丁 |
| Kotaemon https://github.com/Cinnamon/kotaemon | 25796 | 2026-07-14 推送 | 0 | slowing | 积压 221 个 issue、41 个 PR；默认账号 admin/admin、依赖冲突要用户自己处理；纯"和文档对话"已失去差异 |
| wechaty https://github.com/wechaty/wechaty | 23355 | 2025-12-21 推送 | 0 | dead | 发版停在 2021 年；更深的原因普查里没有证据 |
| Coze Studio https://github.com/coze-dev/coze-studio | 21694 | 2026-07-29 | 1 | slowing | 最近 release 是 v0.5.1（2026-02-05），发版明显放缓；原因没有证据 |
| Rasa https://github.com/RasaHQ/rasa | 21345 | 2025-12-18（按提交日期） | 0 | dead | 普查只能确认提交停了；原因没有证据 |
| Parlant https://github.com/emcie-co/parlant | 18298 | 2026-07-10（默认分支 develop；仓库 2026-07-12 有一次分支推送） | 0 | slowing | 3.x 在 3 到 4 月密集发版（v3.3.0 是 03-15，v3.3.2 是 04-28）后停下，外部 PR 一直到 2026-09-29 还有更新；原因没有证据 |
| Ragas https://github.com/explodinggradients/ragas | 15978 | 2026-02-24 推送 | 0 | dead | 仓库已重定向到新组织 vibrantlabsai，可能是迁移造成的假死，需要人工确认 |
| QAnything https://github.com/netease-youdao/QAnything | 14196 | 2026-09-23 推送 | 3 | slowing | 最近 release 是 2024-08，发版间隔很长 |
| XianyuAutoAgent https://github.com/shaxiu/XianyuAutoAgent | 9318 | 2026-06-10 推送 | 0 | slowing | 依赖逆向协议和 Cookie；作者声明仅供学习、可能随时停更；维护者公开求职；没有商业闭环。对照组：有履约和定制收入的 xianyu-auto-reply 还活着 |
| R2R https://github.com/SciPhi-AI/R2R | 8012 | 2025-11-07 推送 | 0 | dead | 普查里没有证据 |
| Papercups https://github.com/papercups-io/papercups | 6116 | 2022-05-10（提交） | 0 | dead | 超过 4 年没有提交 |
| Chaskiq https://github.com/chaskiq/chaskiq | 3573 | 2026-06-30 | 0 | slowing | 发版已停约 3 年 |
| Vocode https://github.com/vocodedev/vocode-core | 3803 | 2024-11-15 推送 | 0 | dead | 约 2 年没有推送 |
| TextGrad https://github.com/zou-group/textgrad | 3753 | 2025-07-25 推送 | 0 | dead | 这个方向被 DSPy、GEPA、SkillOpt 接替（推断，普查没有直接证据） |
| Peppermint https://github.com/Peppermint-Lab/peppermint | 3160 | 2025-09-21 推送 | 0 | dead（已归档） | 已归档 |
| dify-on-wechat https://github.com/hanfangyuan4396/dify-on-wechat | 2836 | 2026-04-03 推送 | 0 | dead | 它是 CowAgent 的下游分支，上游已经转型，并原生覆盖了企微、公众号和微信客服 |
| GoFly LiveChat https://github.com/taoshihan1991/go-fly | 2593 | 2025-09-04 | 0 | dead | 仓库已改名；没有 release |
| HuixiangDou https://github.com/InternLM/HuixiangDou | 2502 | 2025-11-24 推送 | 0 | dead | 普查里没有证据 |
| Invariant https://github.com/invariantlabs-ai/invariant | 467 | 2026-01-12 推送 | 0 | dead | 普查里没有证据 |
| CosyVoice https://github.com/FunAudioLLM/CosyVoice | 23903 | 2026-05-25 推送 | 0 | slowing | 仓库已迁移到新组织，可能是迁移造成的假象，需要人工确认 |

另外有两类项目在"活着"和"放缓"之间：
- **Guardrails AI**：90 天提交只有 32 次。2026-08-25 停了 Hub 托管安装器和免费远程推理，2026-09-09 被 Harvey 收购（金额未披露，团队并入）。据报道，开源框架保留 Apache-2.0 并继续更新，仓库 2026-10-09 仍有推送。但收购方的重心是自家法律 agent，开源库的长期路线没有正式承诺。
- **GraphRAG**：官方声明只接受 bug 和依赖修复。

另有 4 个 6 月后才出现的项目已经放缓：tgo（https://github.com/tgoai/tgo）、openilink-hub（https://github.com/openilink/openilink-hub）、openclaw-china（https://github.com/BytePioneer-AI/openclaw-china），以及若干电商客服助手。

**整体规律**：活下来的项目要么有商业母体，要么有插件或适配器生态飞轮，要么做的是有履约收入的垂直刚需。停掉的多数是"接个大模型回消息"的薄壳，或者是被更强项目吸收了方向的单点工具。Flowise 是例外：它体量很大，仍然是团队主动收尾。可见星数高并不保证延续，交付依赖要看维护方的承诺。

---

## 3. 交叉对比：UNICA 的差异化在开源世界有没有对手

| UNICA 差异点 | 有没有直接对手 | 最接近的项目 | 差距在哪 |
|---|---|---|---|
| **确定性事实注入并在回答后校验** | 没有 | Parlant（glossary、canned response、guideline）；Graphiti（时序事实、溯源）；cognee（本体加溯源） | Parlant 是框架，不是成品，而且在放缓；Graphiti 和 cognee 是记忆底座，不做回答后校验。**UNICA 反过来落后的地方**：事实没有逐条的有效窗口和复核时间（已核实：`unica/pkg/domain/ontology.go` 里的 Assertion 只有 class/scope/values），`doc/metrics-northstar.md` 第 3 节也承认新鲜度只能按版本近似。Graphiti 已经做了双时间轴 |
| **承诺防火墙** | 没有现成的 | promptfoo 的 `contracts`、`ecommerce:price-manipulation` 插件（测试侧）；CrewAI 和 Guardrails AI 的校验失败回灌；Microsoft AGT 的 `require_approval` | 别人只在测试侧或通用动作层，没有"退款、赔付、价格、保证"的业务语义。**UNICA 落后的地方**：动作目前接近"放行或扣下"二选一（已核实），没有"重写一次""模板加槽位作答""审批单放行"这些中间档。离"审批单"最近的是：被扣下的草稿会作为"AI 草稿（确认或修改后可直接发送）"写进转人工备注 |
| **判定证据链（按租户哈希链）** | 没有业务语义级的 | Microsoft AGT（Merkle 审计加决策 BOM、合规映射）；Langfuse 和 OpenLLMetry（调用级 trace） | AGT 是通用的 agent 治理，而且没有具名采用者；Langfuse 只是调用日志。**UNICA 的风险**：AGT 如果以 Dify 插件的形式进入生态（它之前提的集成 PR 被要求改走插件市场），"可审计"会变成默认配置 |
| **统计验证门驱动的自演化** | 概念上已经有对手 | SkillOpt（逐任务不退步、留出验证、夜间 Sleep 引擎、反馈和证据隔离）；DSPy/GEPA；CowAgent 的夜间记忆提炼；WeKnora 的 Wiki 回滚 | SkillOpt 把"门控文本自演化"做成了免费通用库，"会自演化"已经不稀缺。UNICA 的独特之处是**演化对象是确定性事实，要经人工审核，可以回滚，样本不足时会明说**。核查修正：训练侧和留出侧的物理隔离 UNICA **已有**（`unica/router/internal/replay/mine.go` 拒收留出侧样本）；缺的是"语义化失败描述"和"原始判定"的分层 |
| **中国电商渠道（抖音、淘宝、快手、小红书）** | 开源里没有 | 闲鱼工具（逆向协议，不合规）；LangBot、AstrBot、CowAgent、OpenClaw 覆盖企微、公众号、微信客服、飞书、钉钉 | 通用 IM 渠道已经被做成免费品。**UNICA 的缺口**：还没有企微"微信客服"渠道（已核实：`unica/gateway/internal/adapter/wechat` 只实现了公众号），这是中小企业私域客服的主入口 |
| **私有化多租户** | 部分有 | WeKnora（工作区 RBAC、审计日志、Helm 加 Lite）；Wanwu（多租户平台）；Dify 企业版 | 别人是平台级多租户，没有客服业务。**交付风险**：Dify 许可证不允许未经授权用它的源码运营多租户环境，一套部署服务多个租户需要法务确认 |

**结论**：要把 UNICA 的整套差异拼出来，最接近的组合是"Dify + NeMo Guardrails + Langfuse + LangBot + Chatwoot"。这需要很强的集成能力，中小企业自己做不了，但大型集成商可以拿它做定制项目来压价。售前话术应该围绕"拼装方案缺什么"来讲：承诺失控、没有可校验的证据链、没有电商官方渠道、没有受控的改进闭环。

---

## 4. 可借鉴思路清单（按投入小、收益大排序，重复的已合并）

| # | 思路 | 来源项目 | 机制 | 在 UNICA 落在哪 | 工作量 | 风险 | 仓库现状 |
|---|---|---|---|---|---|---|---|
| 1 | **挖掘器反馈与证据隔离** | SkillOpt https://github.com/microsoft/SkillOpt | 优化器只能看到语义化的失败描述，原始裁判理由只进审计日志，防止它学会"刷门" | evolve 的挖掘阶段输入、`router/internal/replay`；原始判定只写证据链 | S | 描述写得太泛会降低挖掘质量 | 【仓库现状：部分。训练和留出的物理隔离已有：`unica/router/internal/replay/mine.go` 的 `Mine` 遇到留出侧样本会直接拒绝整次调用，`split.go` 负责划分。挖掘器的输入是转人工原因码（`c.Logged.Reason`、`ClassifyReason`），不是裁判长文，所以泄漏面本来就小。缺的是"语义化失败描述"和"原始判定"的显式分层】 |
| 2 | **逐用例不退步、被拒建议签名缓冲、单次发布改动上限** | SkillOpt | 任何一个验证任务退步就拦下；被拒的编辑记下来不再重复提；每轮编辑量有上限 | 验证门、`pkg/domain/proposalstore`、审核队列 | S | 门变严后过门率会下降，要在看板上说明 | 【仓库现状：部分。`unica/router/internal/replay/gate.go` 已经做到：必须转人工的对话丢失（ForcedLost）、新出现违规、"肯定了否定项"上升，这几种逐例一票否决；自助解决度用配对符号检验加自助法区间。普通的"已解决变未解决"只列在 `Regressed` 里，不单独拦截。去重只针对**待审**项（迁移 024 的 `idx_ontology_proposals_pending_dedup`，见 `unica/pkg/domain/proposalstore.go`），被拒的建议下一轮还能再提。单轮改动上限没有】 |
| 3 | **自助解决率口径公开：会话级三档，排除放弃会话** | MLflow https://github.com/mlflow/mlflow ；Botpress https://github.com/botpress/botpress | 分成 fully / partially / needs_follow_up 三档；区分"遏制率"和"解决率" | `admin/internal/tenant/quality/signals.go`、`doc/metrics-northstar.md`；口径变更时打断点（参考 tau2 的做法） | S | 历史数据和新口径不可比 | 【仓库现状：部分。口径已公开，样本不足时返回 null 并置 insufficient 标志（`doc/metrics-northstar.md`、`unica/pkg/domain/northstar.go`）。但口径是二值的，算法是"1 − 有转人工决策的会话 / 会话数"，实际是**遏制率**；"未追问纠错"文档里写明暂不可测。没有三档，也没有排除放弃会话】 |
| 4 | **按租户一键"全转人工"开关，加误拦率 SLO 和错误预算** | Agent Governance Toolkit https://github.com/microsoft/agent-governance-toolkit | 紧急开关、SLO、错误预算、熔断 | 熔断器旁边加租户级开关；运营台展示 | S | 开关被误触，需要二次确认并留痕 | 【仓库现状：部分，方向相反。`unica/pkg/domain/breaker.go` 有按产线的熔断器（扣下率 25%、至少 20 个样本、窗口 100、冷却 15 分钟），但它触发后是**关掉执法**，不是全转人工；熔断状态会写进证据链的 `breaker_open`。部署级开关只有"不注入本体"（`router.go` 里的 `SetOntology`）。按租户一键全转人工、SLO、错误预算都没有】 |
| 5 | **校验失败先回灌重写一次，再决定转人工** | CrewAI https://github.com/crewAIInc/crewAI ；Guardrails AI https://github.com/guardrails-ai/guardrails | 把违规原因回给模型重写，最多重试 N 次；动作分为 fix、refrain、reask、escalate | `router/internal/guardrail`；每次重写都进证据链 | S–M | 多一次往返，延迟增加；终裁必须仍是确定性校验 | 【仓库现状：没有。违规后直接转 `human_processing`（`unica/router/internal/routing/router.go` 约 1055 行起）。仓库里唯一的"rewrite"是查询侧的主语延续（`routing/subject.go`），不是回答重写】 |
| 6 | **从本体约束自动派生对抗用例，并打包成"行业包"** | promptfoo https://github.com/promptfoo/promptfoo ；garak https://github.com/NVIDIA/garak | 用 policy 插件从一条策略生成诱导话术；电商、金融、医疗分包；编码绕过探针（谐音、拼音、零宽字符） | `router/internal/eval`、回放集；行业红线词表升级为"红线、承诺类型、对抗用例、报告模板"一包 | S–M | 合成样本会形成自我验证闭环，**必须和真实样本分开统计**（llm-eval-honesty） | 【仓库现状：部分。`unica/pkg/domain/tryrun.go` 的 `DeriveProbes` 已经能从本体派生 fact、denial、requires 三类探针，评分完全确定性。行业红线包已有 6 个：`unica/pkg/domain/redlinepacks/{common,ecommerce,education,finance,medical,realestate}.yaml`。零宽字符归一化已有（`unica/pkg/domain/text.go`）。缺的是诱导、议价类对抗话术生成，谐音和拼音绕过探针，以及把红线包、用例、报告模板打成一个包】 |
| 7 | **坐席三选一标注直接生成挖掘候选** | Langfuse https://github.com/langfuse/langfuse ；Dify 标注回流 | 坐席在会话里标"本该答 / 不该答 / 缺事实" | `gateway/internal/webhook/chatwoot.go` 回流，进 proposals 候选 | S–M | 坐席负担加重，只能做一键，不能写长文 | 【仓库现状：部分。坐席回复已经回流（`chatwoot.go` 计数 `ChatwootAgentRepliesTotal`），并通过 `unica/router/internal/replay/reference.go` 的 `CompareToReference` 和 AI 答案做数字、枚举比对，结果只标记、不判定。违规复核有 false_positive、ontology_wrong 两种结论（`portal/violations.html`）。坐席在 Chatwoot 会话里一键三选一的标注没有】 |
| 8 | **会话归属显式建模，加沉默超时分支** | Chatwoot https://github.com/chatwoot/chatwoot ；XianyuAutoAgent | 分为 AI 持有、人工接管、交还三态，配 Take over 按钮；客户长时间不回时，判断结案还是转人工，转人工始终可达 | `router/internal/state`、`handoff` | S–M | 状态机多了分支，需要回放覆盖 | 【仓库现状：部分。`unica/router/internal/state/machine.go` 有 pending、ai_processing、human_processing、closed 四态，但 human_processing 只能到 closed，**没有交还 AI 的迁移**。Chatwoot 标记 resolved 会关闭会话（`chatwoot.go` 的 `conversation_status_changed`）。沉默处理只有 30 分钟空闲后一律关闭（`state/manager.go` 的 `IdleTimeout`），没有"结案还是转人工"的分支】 |
| 9 | **事实双时间字段加来源标注** | Graphiti https://github.com/getzep/graphiti ；Graphify https://github.com/Graphify-Labs/graphify | 字段 valid_from/valid_to 加 confirmed_at；来源分为"人工录入 / 挖掘后审核 / 待确认"；冲突时旧事实失效而不是删除 | `pkg/domain`、`admin/internal/tenant/facts`；到期自动退出注入；新鲜度精确到逐条 | M | 要改 schema，需要先在 migration-ledger 预留编号；默认值关闭时不能出问题 | 【仓库现状：没有。Assertion 只有 class、scope、values（`unica/pkg/domain/ontology.go`）；只有版本级的发布和回滚。工作区有未提交的 `unica/admin/internal/tenant/facts/diff.go`（版本差异），但不涉及时间字段】 |
| 10 | **高风险意图用"预审模板加本体槽位"作答** | Parlant https://github.com/emcie-co/parlant | strict canned response：只能从预审模板里选，槽位由事实填 | 承诺防火墙在"有事实支撑"时走模板，不用转人工 | M | 模板太死会显得生硬；模板需要人工审核 | 【仓库现状：没有。事实只以"确定性事实"块注入提示词（`unica/pkg/domain/render.go` 的 `factsHeader`），承诺防火墙（`unica/pkg/domain/commitment.go` 的 `CheckCommitments`）只做检查，没有模板作答路径】 |
| 11 | **承诺扣下后变成审批单，坐席一键放行或改写** | Dify Human Input https://github.com/langgenius/dify ；AGT `require_approval` | 表单 token 加按钮决定分支，超时回收 | `handoff` 到 Chatwoot；审批结果写回证据链，并作为"缺失事实"的强信号 | M | 坐席对 AI 草稿形成依赖；放行率本身也要监控 | 【仓库现状：部分。被扣下的草稿和扣下原因已经写进 Chatwoot 转人工备注，标题是"AI 草稿（确认或修改后可直接发送）"（`unica/router/internal/handoff/handler.go` 约 234 到 264 行）；证据链保存 `withheld_answer`。没有一键放行按钮、超时回收，也没有把放行或改写的结论结构化写回证据链】 |
| 12 | **试跑时"临时条目一键转正"，每条回复展开依据** | Chatwoot Captain playground；WeKnora https://github.com/Tencent/WeKnora 引用溯源；Kotaemon 原文高亮 | 每条回复显示命中的事实、规则、版本 | `portal/ontology.html`、`evidence.html`；坐席侧附依据，低相关时标"依据不足" | M | 前端工作量；不能暴露租户之间的数据 | 【仓库现状：部分。"发布前试跑"已有（`portal/ontology.html`、`unica/admin/internal/tenant/facts/tryrun.go`，针对草稿本体逐条跑探针）。证据页逐条展示 claims、violations、commitments、sources 和本体版本（`portal/evidence.html`、`unica/admin/internal/tenant/evidence/evidence.go`）。缺的是"临时条目转正"这个交互，以及坐席侧每条回复附依据、"依据不足"标记】 |
| 13 | **售前"历史对话体检报告"** | Botpress（工单可解决率审计） | 客户导出一个月历史，跑出可自助比例和缺失事实清单 | 复用脱敏导出和挖掘链路 | S–M | 客户数据合规，需要签数据处理协议；报告不能当成效果承诺 | 【仓库现状：部分。底层链路已有：脱敏导出说明（`doc/evolve-export.md`，面向客户的一页纸）、`unica/router/internal/replay/{export,redact,mine,report}.go`，可以产出缺失事实工单和聚类报告。缺的是面向售前的报告成品（可自助比例加缺失事实清单的模板）】 |
| 14 | **决策 BOM，定期导出 Merkle 根** | Agent Governance Toolkit | 每次判定记下本体、词表、应用的版本，熔断状态和事实 ID；根交给客户自己存 | `admin/internal/tenant/evidence` | M | 措辞只能说"可校验"，不能宣称"无法改动" | 【仓库现状：大部分已有。证据链每行已经记录 ontology_version、facts_injected、validation_mode、breaker_open、enforced、claims、sources、config_sha256、prev_hash、row_hash，并提供 verify 和 head 接口（`unica/admin/internal/tenant/evidence/evidence.go`）；代码注释明确只说"能发现篡改"，不宣称"无法改动"。缺的是：定期把链头哈希推给客户自存；红线包、Dify 应用版本单列字段（目前可能只被 config_sha256 间接覆盖，没有逐项核实）】 |
| 15 | **企微"微信客服"渠道** | LangBot https://github.com/langbot-app/LangBot （Apache-2.0，可以参考代码）；openclaw-china（许可证不明，只能借设计） | 回调验证、sync_msg 拉取、enter_session 欢迎语 | `gateway/internal/adapter` | M | 企微接口限额；不碰个人号协议 | 【仓库现状：没有。`unica/gateway/internal/adapter/wechat/adapter.go` 只实现公众号（Official Accounts），全仓没有 sync_msg、open_kfid 这类实现】 |
| 16 | **渠道适配器统一接口、契约测试、支持等级表** | NoneBot2 https://github.com/nonebot/nonebot2 ；Botpress integrations | 适配器和框架解耦；公开"渠道、支持等级、最后验证日期" | `gateway/internal/adapter` 抽象；写进交付文档 | M | 重构已有渠道有回归风险 | 【仓库现状：部分。统一接口已有：`unica/gateway/internal/adapter/adapter.go` 的 `ChannelAdapter`（VerifyWebhook、ParseInbound、FormatOutbound、SendMessage），加上 `registry.go`，5 个渠道各自带单测。没有跨适配器的通用契约测试套件，也没有"支持等级、最后验证日期"表】 |
| 17 | **租户 × 渠道的"策略包"** | LangBot 流水线 | 本体子集、红线词表、防火墙阈值、Dify 应用打成一个可配置对象 | `router/internal/routing`，门户可视化编辑 | M | 配置组合爆炸，需要默认继承 | 【仓库现状：大体已有。渠道路由到"产线"（`unica/router/internal/routing/cache.go` 的 `RouteConfig`：Dify 应用、密钥，加上 config_json）；产线 config_json 里带 inject_facts、validation、breaker、commitment、commitment_scope、redline_packs（`unica/pkg/domain/config.go`），门户有 `portal/ai-settings.html`、`portal/channels.html`。产线本身就是这里说的策略包，缺的只是跨产线的默认继承】 |
| 18 | **每日或每周运营日报推送** | LobeHub https://github.com/lobehub/lobehub 的汇报；CowAgent 的夜间批次 | 汇总自助率、转人工原因前 N、被扣下的会话、待审建议 | 推到企微或飞书；配合现有的注意力信号 | S | 样本少时要写"样本不足"，不能编趋势 | 【仓库现状：已有。`unica/admin/internal/opsdigest/`：每日待办（违规统计、待审建议数、可疑版本）加每周北极星小节，通过 webhook 推送（按 Alertmanager 适配器格式），不配 `OPS_DIGEST_WEBHOOK_URL` 时完全不运行。北极星数据沿用"样本不足时返回 null"的规则。是否已对接企微或飞书机器人，取决于部署侧的 webhook 适配器，本仓库外未核实】 |
| 19 | **售前 Lite 单进程演示版** | WeKnora Lite；AnythingLLM https://github.com/Mintplex-Labs/anything-llm | 内存流替代 Redis，mock 替代 Dify，笔记本上跑通全链 | 新建 demo 入口，不进主线 | M | 演示版和生产版行为不一致 | 【仓库现状：没有。只有本地全栈脚本 `scripts/start-local-env.sh`、`scripts/setup-wsl-env.sh`，依赖真实的 Redis、Postgres、Dify、Chatwoot】 |
| 20 | **订单、物流等只读的"事实查询动作"** | n8n https://github.com/n8n-io/n8n （Koralplay 先查交易再回复，厂商自述）；NeMo Guardrails execution rail | 承诺前先查确定性事实，查得到就放行 | Router 动作层，配动作白名单和参数校验 | L | 平台接口授权；只能只读；故障时必须降级为转人工 | 【仓库现状：没有。`unica/router/internal/intent/classifier.go` 把"订单、快递、物流、单号"等归为 transactional，直接转人工，没有任何查询动作】 |
| 21 | **裁判与人工一致率作为准入门槛** | DSPy https://github.com/stanfordnlp/dspy （Nubank 案例）；MLflow 裁判注册 | 带版本的判定器，用审核结论校准，一致率达标才有投票权 | 模型参与的判定器；版本号写进证据链 | M | 需要足够多的人工标签，目前没有 | 【仓库现状：没有，目前也不需要。所有判定都是确定性的（`unica/pkg/domain/tryrun.go` 的 `GradeProbe` 注释写明"never asks a model to judge another model's answer"；校验器、承诺防火墙、门都是规则加统计），仓库里没有模型判定器。等将来引入模型判定器时再把这条作为前置条件】 |
| 22 | **Sitemap / RSS 订阅驱动"事实可能过期"建议** | PandaWiki https://github.com/chaitin/PandaWiki | 订阅客户官网政策页和价目页的变化 | proposals 新增一类来源 | M | 网页解析不稳定，只能出建议，不能自动改 | 【仓库现状：没有。全仓没有 sitemap、rss、爬取相关实现；proposals 的来源只有挖掘（A 级别名、C 级缺失事实）】 |

暂缓：跨渠道"顾客记忆"（Mem0 https://github.com/mem0ai/mem0），工作量 L。它能延续"上次答应过我"这类上下文，但**绝不能当作承诺防火墙放行的依据**。等多渠道身份合并做完以后再考虑。

**核查后的优先级调整**：第 14、17、18 条基本已实现，应移出排期，只补缺口（链头推送、产线默认继承、企微或飞书适配）。真正空白、值得先做的是第 5（重写一次）、9（双时间字段）、10（模板加槽位）、15（企微微信客服）、2 的后两项（被拒签名缓冲、单轮改动上限）。

---

## 5. 落地场景排序

**开源项目有公开证据的落地场景**（厂商自述的已标注）：
- 支付和小商户客服：SumUp 自述"50% 对话分流给 AI"，同时省下 30% 外包客服成本，月请求约 30 万次不再需要人工（Langfuse 案例页 https://langfuse.com/users/sumup ，已复核；注意这是**分流率**，不是解决率）。Koralplay 自述工单自动化 70%（n8n）。
- 银行客服：BPCE、BNP Paribas（Giskard，上线前红队加持续回归）；Nubank（DSPy 论文，评测驱动）。
- 电信客服：Amdocs（NeMo Guardrails）。
- 零售门店：Lowe's（NeMo Guardrails）。
- 政务统一受理：巴西 Campinas 市、DPESP（Rocket.Chat）。
- 企业内部知识助手和 FAQ：PandaWiki 有具名客户墙；Open WebUI 有企业客户。
- 电商个人卖家值守：两个闲鱼工具星数分别为 9318 和 7501，说明需求真实，但方案不合规。

共同点是**问题集中、事实能结构化、留痕有要求**，而且做法都是"上线前红队加业务专家定义正确答案加持续回归"。具名的**中国企业对外客服**案例几乎没有，说明"敢不敢让 AI 直接答"仍然是痛点。

**按 适配度 × 付费意愿 × 证据强度 排序**（每项 1 到 5 分，打分是判断，不是普查数据）：

| 名次 | 场景 | 适配 | 付费 | 证据 | 积 | 切入打法 |
|---|---|---|---|---|---|---|
| 1 | **电商品牌店、旗舰店的售前和售后承诺管控**（退款、价保、赠品、发货时效） | 5 | 4 | 3 | 60 | 用 promptfoo 的 ecommerce 类对抗用例公开自测，结果写进交付文档；首屏演示"议价、价保诱导被拦下"；本体按 SKU 挂载事实（参考 xianyu-auto-reply 的商品专属回复）；售前用"历史对话体检报告"；第二阶段加订单和物流只读查询来提高自助率 |
| 2 | **金融周边和支付服务商的小商户客服**（费率、到账时效、资质） | 3 | 4 | 4 | 48 | 对标 Giskard 和 SumUp 的叙事："上线前红队、持续回归、证据链可交给审计"；卖"月度合规体检"续费；合规映射表的条款要法务核实 |
| 3 | **教育培训、留学招生咨询**（提分、保过、录取承诺红线） | 5 | 4 | 2 | 40 | 行业红线包的第一个样板（`education.yaml` 已有）；入口走公众号和企微微信客服（需要先补这个渠道）；运营人员自己写"禁止回答"用例，进回放集 |
| 4 | **医美、口腔、健康连锁**（疗效承诺、资质展示） | 5 | 4 | 2 | 40 | 资质类事实加有效期（双时间字段），过期自动下线；AI 身份标识和转人工可达作为合规卖点；小红书和抖音是主渠道 |
| 5 | **本地生活和连锁门店**（营业时间、价目、预约、活动期政策） | 4 | 3 | 3 | 36 | 活动类事实用 valid_to 自动到期；按门店分事实；参考 Rocket.Chat 按地域分队列；坐席少，靠日报（已有）和一键接管 |

不推荐作为首发的：企业内部 FAQ 助手（证据最强，但 UNICA 是对外客服，PandaWiki、WeKnora 几乎免费）；政务受理（证据强，但国内采购周期长）。

---

## 6. 不建议跟的方向

1. **铺通用 IM 渠道（个人微信、QQ、海外 IM）。** LangBot、AstrBot、CowAgent、OpenClaw 已经把这些做成免费品；个人号协议对企业客户有法律风险。只补企微微信客服，资源集中在电商官方接口上。
2. **逆向协议和 Cookie 接入。** XianyuAutoAgent 因此放缓；这类方案不能出现在合同交付里。
3. **自研检索或图谱引擎来拼检索质量。** LightRAG、RAGFlow、GraphRAG 投入很大，而且 GraphRAG 已经进入维护模式；检索质量不是 UNICA 的护城河。需要时把 RAGFlow 或 WeKnora 当作可选后端就行。
4. **训练模型权重的强化学习**（Agent Lightning、ART）。需要 GPU，中小企业私有化负担不起，也和"冻结模型、演化本体"的路线冲突。
5. **把自演化或通用护栏单独卖。** SkillOpt 已经免费；OpenPipe 被收购后关停了托管平台（本次未复核）；Guardrails AI 2026-08-25 停了托管推理，2026-09-09 被收购。"训练或优化即服务"和"通用护栏"都很难单独成为生意，要打包在"可自证客服"里一起交付。
6. **往通用 Agent 助手或企业内部助手的方向转型**（OpenClaw、LobeHub、Open WebUI 的赛道）。和 UNICA 的对外合规定位冲突，而且是红海。
7. **在运营门户里提供任意代码执行或可视化编排节点。** 第三方报道 Langflow 的高危漏洞被持续利用（来源可信度存疑）；Flowise 也有公开的远程代码执行安全通告，而且归档后不再出补丁。UNICA 的管理面默认只在内网监听，这本身就是卖点。
8. **为了换监控栈改业务代码，或者把 Python 评测栈塞进交付物。** 这违反平台"Netdata、服务侧零改动"的约定。OTel 导出只做成可选、默认关闭；观测组件打包时优先选 Apache-2.0 的 Opik，Phoenix 是 ELv2，要先过法务。
9. **现在做语音客服。** Pipecat、LiveKit 都很活跃，但 UNICA 的渠道全是文字，语音一侧的 Vocode 已经停更、CosyVoice 在放缓，普查里也没有语音客服落地的证据。暂缓。
10. **在不清楚许可证的情况下复用代码。** AstrBot、PandaWiki、Bytedesk、xianyu-auto-reply 是 AGPL，XianyuAutoAgent 是 GPL，只能借设计；openclaw-china 和 Customer-Agent 没有声明许可证。Dify 的多租户条款和 Chatwoot 的 enterprise 边界，交付前都必须核实。

---

**数据局限**：没有 6 月星数基线，所以没法算增长；90 天提交显示 100 的项目实际可能更多；各案例数字大多是厂商自述；部分项目的许可证显示 NOASSERTION（包括 Flowise），需要人工看 LICENSE 原文。有几个仓库的名字本身就是模型名（安全分类模型、模型厂商的客服演示等），按文档规范没有列进表里，它们都是 dead 或低相关，不影响结论。

---

## 核查记录

**A. 仓库核查（只读，2026-10-09，包括工作区未提交的改动）**

| # | 结论 | 主要证据 |
|---|---|---|
| 1 | 部分 | `unica/router/internal/replay/mine.go`（拒收留出侧）、`split.go` |
| 2 | 部分 | `unica/router/internal/replay/gate.go`、`unica/pkg/domain/proposalstore.go`、`unica/router/migrations/024_ontology_proposals.sql` |
| 3 | 部分 | `doc/metrics-northstar.md`、`unica/pkg/domain/northstar.go` |
| 4 | 部分（熔断方向相反） | `unica/pkg/domain/breaker.go` |
| 5 | 没有 | `unica/router/internal/routing/router.go`、`routing/subject.go` |
| 6 | 部分 | `unica/pkg/domain/tryrun.go`、`unica/pkg/domain/redlinepacks/*.yaml`、`unica/pkg/domain/text.go` |
| 7 | 部分 | `unica/gateway/internal/webhook/chatwoot.go`、`unica/router/internal/replay/reference.go` |
| 8 | 部分 | `unica/router/internal/state/machine.go`、`state/manager.go` |
| 9 | 没有 | `unica/pkg/domain/ontology.go` |
| 10 | 没有 | `unica/pkg/domain/render.go`、`commitment.go` |
| 11 | 部分 | `unica/router/internal/handoff/handler.go` |
| 12 | 部分 | `portal/ontology.html`、`portal/evidence.html`、`unica/admin/internal/tenant/facts/tryrun.go` |
| 13 | 部分 | `doc/evolve-export.md`、`unica/router/internal/replay/` |
| 14 | 大部分已有 | `unica/admin/internal/tenant/evidence/evidence.go` |
| 15 | 没有 | `unica/gateway/internal/adapter/wechat/adapter.go` |
| 16 | 部分 | `unica/gateway/internal/adapter/adapter.go`、`registry.go` |
| 17 | 大体已有 | `unica/router/internal/routing/cache.go`、`unica/pkg/domain/config.go` |
| 18 | 已有 | `unica/admin/internal/opsdigest/` |
| 19 | 没有 | `scripts/` |
| 20 | 没有 | `unica/router/internal/intent/classifier.go` |
| 21 | 没有（目前无模型判定器，不适用） | `unica/pkg/domain/tryrun.go` 的 `GradeProbe` |
| 22 | 没有 | 全仓检索无相关实现 |

统计：已有或大部分已有 3 条，部分 12 条，没有 7 条。

**B. 证据最弱的论断复核**

1. **"Flowise 已归档，归档原因普查里没有证据"：后半句错误，已修正。** 仓库确实已归档（接口返回 archived=true，推送时间 2026-08-13，55496 星，许可证 NOASSERTION），而且原因有官方说明。Discussion #6727 "The Future of Flowise"（https://github.com/FlowiseAI/Flowise/discussions/6727 ）给出了时间线：2026-07-29 冻结代码，2026-08-13 归档并弃用 npm 包和镜像，2026-08-31 核心团队退出，鼓励按 Apache-2.0 fork。第三方评测称 Flowise Cloud 将于 2026-11-30 关停，这一点没有在官方公告中找到，未采信。
2. **"Guardrails AI 被 Harvey 收购，开源库后续怎么维护没有说明"：收购属实，后半句需要修正。** Harvey 官方博客（https://www.harvey.ai/blog/guardrails-ai-joins-harvey ）和 Unite.AI、The Next Web 的报道显示：收购于 2026-09-09 宣布，是 Harvey 当年第 4 起收购，金额未披露；Hub 托管安装器和免费远程推理在 2026-08-25 退役。据报道开源框架保留 Apache-2.0 并继续更新，仓库 2026-10-09 仍有推送。已改写为"保留并在更新，但没有长期承诺"。
3. **"Parlant 90 天 0 提交、放缓"（结论摘要第 1 条的核心论据）：属实。** 默认分支 develop 的最后一次提交是 2026-07-10；2026-07-11 之后没有提交。仓库 pushed_at 是 2026-07-12，来自一个特性分支。最近的 release 是 v3.3.2（2026-04-28）、v3.3.1（04-14）、v3.3.0（03-15）。外部 PR 最近一次更新在 2026-09-29。原文"1 到 4 月密集发版"按 3.3.x 的数据收窄为"3 到 4 月"，2 月以前的版本没有逐一核对。

附带复核：
- SumUp 的 50%：属实，见 Langfuse 案例页 https://langfuse.com/users/sumup 。原文是 deflection（分流），同时省下 30% 外包成本，属于厂商自述，页面约在 2025 年发布。
- promptfoo 被收购：属实，2026-03-09 宣布，项目继续开源（收购公告见 promptfoo 官方博客）。正文按文档规范保留"头部模型厂商"的写法。
- 星数抽查（2026-10-09 实时）：OpenClaw 391538、LangBot 18071、SkillOpt 18171，与表中数值相差不超过 3 颗，没有改动。

**C. 本次未核实、仍需人工确认的**：OpenPipe 被收购后关停托管平台；Koralplay 的 70%（n8n）；Langfuse 被 ClickHouse 收购；opsdigest 的 webhook 是否已在部署侧对接企微或飞书；证据链 config_sha256 是否覆盖红线包和 Dify 应用版本。

**来源**：
- [The Future of Flowise（Discussion #6727）](https://github.com/FlowiseAI/Flowise/discussions/6727)
- [Guardrails AI Joins Harvey](https://www.harvey.ai/blog/guardrails-ai-joins-harvey)
- [Harvey Acquires Guardrails AI（Unite.AI）](https://www.unite.ai/harvey-acquires-guardrails-ai-its-fourth-acquisition-of-2026/)
- [Harvey $550M 融资并收购 Guardrails AI（The Next Web）](https://thenextweb.com/news/harvey-550m-round-15-6bn-guardrails-acquisition)
- [SumUp 案例（Langfuse）](https://langfuse.com/users/sumup)
- GitHub 接口实时数据：FlowiseAI/Flowise、emcie-co/parlant、guardrails-ai/guardrails、langbot-app/LangBot、openclaw/openclaw、microsoft/SkillOpt
