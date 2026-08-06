# Active Task: （无进行中任务——五期全部完成）

第 5 期部署演练已完成（2026-08-06，WSL）。全部五期交付完毕，
`feat/answer-quality-phase0` 分支待合并。

## 演练环境（保留运行，可直接使用）
- Dify 0.15.3：http://localhost:3402（控制台 admin@unica.local / Rehearsal-Dify-2026!）
  三应用已切 deepseek-v4-flash（经 openai_api_compatible provider）
- 运营门户：http://localhost:3403（rehearsal@unica.local / Rehearsal-2026!）
  admin :8082 / router :8081 为 WSL 主机进程（~/unica-run/，日志同目录）
- 业务库：unica-dify-postgres 容器内 `unica` 库（14 个迁移全应用）；
  Redis：unica-redis :6380
- 已释放：k3d 集群（旧 dev 基础设施）、unica-pg（全新重放验证用）、远程预览（7月已拆）

## 剩余未验证事项（全部需要真实客户流量，见 doc/unverified.md）
shadow 实测覆盖率/误报率、业务方独立写本体 + 真实日志黄金集、
熔断阈值与告警阈值标定、token 成本对账、多副本行为、语义修正的真实场次观察。

## 建议下一步
1. 合并分支（gh pr create 或直接 merge 到 main）
2. 小红书渠道接真实凭证，开 shadow 跑一到两周（unverified §一）
3. channels 死表移除迁移（unverified §3.5.6）
