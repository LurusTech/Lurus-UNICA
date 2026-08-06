# Active Task: （无进行中任务——五期全部完成）

第 5 期部署演练已完成（2026-08-06，WSL）。全部五期交付完毕，
`feat/answer-quality-phase0` 分支待合并。

## 演练环境（容器已释放，数据保留可复原）
2026-08-06 演练结束后按指示释放全部 WSL 容器（dify 七件套、unica-portal、
unica-redis）并停掉 admin/router 主机进程。**数据目录 /data/unica-dify
（pgdata 含业务库与全部演练数据）与镜像保留在磁盘**，恢复方式：
`cd /data/unica-dify && docker compose up -d`，再按 ~/unica-run/ 里的
二进制与第 5 期脚本（jobs tmp 已随任务清理，命令在 git 历史与本文档）起服务。
凭证：Dify 控制台 admin@unica.local / Rehearsal-Dify-2026!；
门户超管 rehearsal@unica.local / Rehearsal-2026!。
早前已释放：k3d 集群、unica-pg、远程预览两台（7 月已拆）。

## 剩余未验证事项（全部需要真实客户流量，见 doc/unverified.md）
shadow 实测覆盖率/误报率、业务方独立写本体 + 真实日志黄金集、
熔断阈值与告警阈值标定、token 成本对账、多副本行为、语义修正的真实场次观察。

## 建议下一步
1. 合并分支（gh pr create 或直接 merge 到 main）
2. 小红书渠道接真实凭证，开 shadow 跑一到两周（unverified §一）
3. channels 死表移除迁移（unverified §3.5.6）
