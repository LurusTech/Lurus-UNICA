# 本地嵌入模型服务

给 Dify 提供一个 OpenAI 兼容的 `/v1/embeddings` 端点，用本地 BGE 中文模型，
不依赖任何外部厂商与额外 API key。

## 为什么需要它

Dify 的知识库有两种索引方式：

| | high_quality | economy |
|---|---|---|
| 索引 | 每个分段做向量嵌入 | 每个分段抽取有限个关键词建倒排 |
| 检索 | 按语义匹配 | 按关键词匹配 |
| 依赖 | 需要嵌入模型 | 无 |

economy **不是** high_quality 的廉价等价物。它只能匹配抽词器挑中的那些词，
没被挑中的词无论在文档里出现多少次都检索不到——中文里这包括绝大多数专有名词
和复合词（它们不是单个 token）。实测（2026-08-12，房产知识库，16 篇文档）：

```
查询「青溪墅园」→ 零命中     （该词在目标文档中出现 3 次）
查询「物业费」  → 零命中     （5 篇房源文档都含此词）
```

更糟的是它不会报错：上传成功、索引 completed、知识库看起来是健康的，
但大量真实问题会得到"我这边没有这个信息"，或者拿到**邻近记录的数据**——
把 A 楼盘的物业费当成 B 楼盘的答出来。

换成本服务提供的语义检索后，同样的查询目标文档均排第一。

问题在于：部署方通常已经在用的那个大模型厂商往往不提供嵌入端点
（例如 DeepSeek 就没有）。为一个几百 MB 的小模型再引入一家厂商、
再管一把 key 不划算，所以这里在本地起一个，用 Dify 已经支持的
「OpenAI-API-compatible」协议接入，Dify 侧零改动。

## 模型

默认 `BAAI/bge-base-zh-v1.5`：768 维，中英双语（面向中文训练），约 400MB，
CPU 上足够应付索引与查询负载。换模型改环境变量 `EMBEDDING_MODEL` 即可，
任何 sentence-transformers 能加载的模型都行。

输出向量做了 L2 归一化——这是 BGE 的要求（点积即余弦相似度），
而 Dify 用余弦比较。

> BGE 官方建议给**查询**（而非文档）加一句检索指令前缀。Dify 把两者
> 都发到同一个端点且不作区分，给文档加前缀会有害，因此这里不加。
> v1.5 系列本身就是可以不加前缀使用的。

## 运行

```bash
# 依赖：torch、transformers、sentence-transformers、fastapi、uvicorn、numpy
EMBEDDING_PORT=8199 python server.py
```

首次运行会从 HuggingFace 拉模型（约 400MB）并缓存到 `~/.cache/huggingface`；
之后离线可用。模型是**懒加载**的——端口立刻可用，第一次调用时才加载，
所以健康检查不必等待模型就绪。

环境变量：

| 变量 | 默认 | 说明 |
|---|---|---|
| `EMBEDDING_MODEL` | `BAAI/bge-base-zh-v1.5` | 模型名 |
| `EMBEDDING_PORT` | `8199` | 监听端口 |
| `EMBEDDING_HOST` | `0.0.0.0` | 监听地址 |
| `EMBEDDING_BATCH_SIZE` | `16` | 批大小，限制索引时的峰值内存 |

端点：`POST /v1/embeddings`、`GET /v1/models`、`GET /health`。

## 接入 Dify

控制台 → 设置 → 模型供应商 → **OpenAI-API-compatible** → 添加模型：

| 字段 | 值 |
|---|---|
| 模型类型 | Text Embedding |
| 模型名称 | `BAAI/bge-base-zh-v1.5` |
| API endpoint URL | `http://<本服务地址>:8199/v1` |
| API Key | 任意非空值（本服务不校验） |
| 模型上下文长度 | 512 |
| 最大分块数 | 16 |

**地址怎么填**取决于 Dify 与本服务的相对位置：

- 同一台 Linux 主机、Dify 在 Docker 里 → `http://172.17.0.1:8199/v1`（docker0 网关）
- Dify 在 WSL2 的 Docker 里、本服务跑在 Windows 宿主 → WSL 网关地址，
  用 `wsl bash -lc "ip route | grep default"` 查（本机为 `172.17.144.1`）
- 同一 compose 网络 → 服务名

接入后需要让 UNICA 也切过去：把 admin 的 `DIFY_INDEXING_TECHNIQUE`
改成 `high_quality` 并重启，否则新上传的文档仍按 economy 建索引。

## 已有知识库的迁移

切换索引方式不会自动重建已有文档的索引。对每个已存在的知识库：

1. 控制台 PATCH 数据集：`indexing_technique=high_quality`、
   `embedding_model=BAAI/bge-base-zh-v1.5`、
   `embedding_model_provider=openai_api_compatible`，
   检索方式改 `semantic_search`
2. 删除并重新上传该知识库的全部文档（走 UNICA 的
   `POST /api/v1/ai-config/{id}/knowledge/documents`，让平台当前的分段
   与索引设置生效）
3. 确认 `indexing_status` 全部 `completed`

## 生产部署注意

本服务当前形态适合预览/演练环境。上生产前需要：

- 放进 compose/k8s 由编排器管理生命周期与重启，不要手工 `nohup`
- 加鉴权（现在接受任意 key），或用网络策略限制只有 Dify 能访问
- 按并发量评估 CPU；索引大批文档时是 CPU 密集的，必要时用 GPU 镜像
  或换 `bge-small-zh-v1.5`（512 维，更快，质量略降）
