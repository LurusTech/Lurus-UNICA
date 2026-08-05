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

## 这套环境暴露过的问题（均已修）

- **Dify 应用配置只能整体提交**：`POST /console/api/apps/{id}/model-config`。
  原先 `dify_admin.go` 往 `PUT /apps/{id}` 发 `{"pre_prompt": ...}`，那是应用**改名**接口，
  真实 Dify 0.15.3 返回 400 `Missing required parameter ... name`，即便补上 `name` 也不会改提示词。
  现在两边都是先读回当前对象、只改提示词与变量声明再整体写回。
  可用真实 console 验证：`go test ./internal/bridge/ -run LiveDify`（见该测试头部的环境变量）。
- **迁移 001 最后一条语句必然失败**：唯一索引建在分区表上却不含分区键 `created_at`，
  所以入站去重索引在任何环境都不存在。现由 `012` 在每个叶子分区上建。
- **分区枯竭**：`messages` 只到 2026-04、`audit_logs` 只到 2026-06，当月写入直接失败。
  现由 router 自动续期，见根 README 的「分区与保留」。
