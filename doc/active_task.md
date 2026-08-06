# Active Task: （无进行中任务——客户自助层四期全部完成）

客户自助层（分支 feat/customer-self-service，2026-08-06）四期交付完毕：
1. 知识库后端（数据集级密钥 + 修复 app key 误用与 batch 误用两处根因，迁移 015）
2. portal 知识库页 + 单线客户视图（JWT 解码、选择器锁定、角色卡片隐藏）
3. 一键开户（POST /api/v1/customers，Chatwoot 渐进落盘可收敛重试，迁移 016）
4. WSL 复原环境实测 17/17 + 界面冒烟全通；audit 脱敏实证；
   新增 DIFY_INDEXING_TECHNIQUE（无嵌入模型的工作区须设 economy）

租户模型定案：1 账号 = 1 产品线（开户约定，表结构保持多对多）；
一线多渠道；同浏览器多标签页可双开（token 按标签页隔离）；
本体编辑暂不下放客户（首页对非超管隐藏入口）。

## 演练环境（当前在 WSL 运行中，未释放）
- Dify 栈：/data/unica-dify，nginx :3402（控制台 admin@unica.local / Rehearsal-Dify-2026!）
- admin：宿主进程 :8081（~/unica-run/admin.new + admin.env）；unica-redis :6380
- portal：unica-portal 容器（host 网络 :8791），Windows 侧用 WSL IP 访问
  （如 http://172.17.158.157:8791，IP 以 `wsl hostname -I` 为准）
- 门户超管 rehearsal@unica.local / Rehearsal-2026!；演练客户 DrillCo3/4/5
  （一次性密码未留存，要用就重新开户）
- 数据集密钥为演练自铸（admin.env 内），生产部署须自行铸造并轮换
- 释放：`pkill -f unica-run/admin; docker rm -f unica-portal unica-redis;
  cd /data/unica-dify && docker compose down`（数据目录保留即可复原）

## 建议下一步
1. 合并 feat/customer-self-service 到 main（CI 已配置六模块矩阵）
2. 真实 Chatwoot 环境跑一次开户 + 打断重试（unverified §3.7.1）
3. 小红书真实凭证 + shadow 一到两周（unverified §一，未动）
4. channels 死表移除迁移（unverified §3.5.6，未动）
