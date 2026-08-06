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

## 演练环境（2026-08-06 已按指示释放，数据保留可复原）
容器与进程全部停止（dify 七件套、unica-portal、unica-redis、admin 宿主进程），
**数据目录 /data/unica-dify（85MB，业务库含 DrillCo3/4/5 演练客户）与
~/unica-run/（admin.new 为本次分支构建，admin.env 含演练自铸数据集密钥）保留**。
复原：`cd /data/unica-dify && docker compose up -d`，重建 unica-redis(:6380) 与
unica-portal（host 网络 :8791，配置见 git 历史 p4_run 脚本模式），
`source ~/unica-run/admin.env` 后起 admin.new。
凭证：Dify 控制台 admin@unica.local / Rehearsal-Dify-2026!；
门户超管 rehearsal@unica.local / Rehearsal-2026!（演练客户一次性密码未留存）。
Windows 侧访问一律用 `wsl hostname -I` 的 IP，localhost 转发不通。

## 建议下一步
1. 合并 feat/customer-self-service 到 main（CI 已配置六模块矩阵）
2. 真实 Chatwoot 环境跑一次开户 + 打断重试（unverified §3.7.1）
3. 小红书真实凭证 + shadow 一到两周（unverified §一，未动）
4. channels 死表移除迁移（unverified §3.5.6，未动）
