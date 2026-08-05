# Dify 预览环境（本地 WSL / 远端主机通用）

一份 compose 同时服务本地和远端，差异全在 `.env`。Dify 固定 0.15.3——router 的 bridge 代码是对着这个版本写和测的。

## 一次性部署

WSL 里 Docker 若是原生安装（非 Docker Desktop）且 PID 1 不是 systemd，daemon 要手动起：

```bash
service docker start          # 每次 WSL 重启后都要
```

然后：

```bash
DST=/data/unica-dify
mkdir -p $DST/{pgdata,storage,initdb}
cp docker-compose.yml nginx.conf proxy_params_dify *.py $DST/
cp initdb/*.sql $DST/initdb/
cp .env.example $DST/.env        # 改里面的地址与密钥

cd $DST && docker compose up -d
```

`initdb` 会同时建 `dify_db` 与 `unica` 两个库（预览环境共用一个 postgres 实例，生产不要这么干）。
postgres 映射到宿主机 `5433`，供 `cmd/evalset` 与 `cmd/ontology` 从 Windows 侧直连。

## 引导

```bash
cd /data/unica-dify

# 1. 管理员（首次）
curl -s -X POST http://localhost:3402/console/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@unica.local","name":"UNICA Admin","password":"<你的密码>"}'

# 2. 每条产品线一个应用 + service API key（幂等，可重复跑）
DIFY_PASSWORD=<密码> python3 bootstrap_apps.py MegaStore FreshMart TechZone

# 3. 模型 provider + 提示词编排
DIFY_PASSWORD=<密码> DEEPSEEK_API_KEY=<key> python3 configure_apps.py

# 4. 把绑定写进 product_lines
python3 seed_product_lines.py
```

## UNICA 侧

```bash
cd unica/router
for f in migrations/*.sql; do
  docker exec -i unica-dify-postgres psql -U dify -d unica -v ON_ERROR_STOP=1 < $f
done

export POSTGRES_URL="postgres://dify:<密码>@localhost:5433/unica?sslmode=disable"
go run ./cmd/ontology publish -dir ../../deploy/config/ontology
```

## 验证

```bash
go run ./cmd/evalset -intent-triage on -inject-facts=false -save-baseline base.json   # 对照组
go run ./cmd/evalset -intent-triage on -inject-facts=true  -baseline base.json        # 实验组
```

## 已知问题

- **`configure_apps.py` 用的是 `POST /console/api/apps/{id}/model-config`**，
  而 `router/internal/bridge/dify_admin.go` 的 `UpdateAppConfig` 用 `PUT /apps/{id}` 只传 `pre_prompt`，
  真实 Dify 0.15.3 返回 400。正确做法是整个 model_config 对象一起提交——部分更新不被接受。
  修 `dify_admin.go` 时照 `configure_apps.py` 改。
- **迁移 001 的最后一条语句在 PostgreSQL 上必然失败**：
  `CREATE UNIQUE INDEX idx_messages_platform_msg ON messages(platform_msg_id)` 建在分区表上，
  而唯一索引必须包含分区键 `created_at`。结果是**入站消息去重的唯一索引根本不存在**。
- **迁移 001 只建了 2026-03 与 2026-04 两个 messages 分区**，之后的月份没有分区，
  写入直接报 `no partition of relation "messages" found for row`。需要补分区或改用默认分区 + 定期滚动。
