# Active Task: （无进行中任务）

第 3 期（证据闭环与告警，commit e754f92）与第 4 期（坐席省力 + 售前分诊 +
渠道防呆，commit 1eba232）均已交付，CI 与 `-race` 全绿。

下一增量（第 5 期，最后一期）：部署演练——WSL/预览环境重部署（迁移 013 重放、
Grafana configmap 重生成）、DeepSeek 模型接入 Dify 供测试（环境变量
deepseek-api-key，模型 deepseek-v4-flash）、黄金集重跑存新基线、
portal 三页对真实 admin 联调、模拟多轮对话演练。完成后剩余未验证事项
只剩"真实客户流量"一类（见 doc/unverified.md）。
