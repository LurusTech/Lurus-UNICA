#!/usr/bin/env bash
# Bring up the whole local UNICA stack, or report on it.
#
#   wsl bash /mnt/e/kefu/scripts/start-local-env.sh            # start everything
#   wsl bash /mnt/e/kefu/scripts/start-local-env.sh --status    # report only
#   wsl bash /mnt/e/kefu/scripts/start-local-env.sh --stop      # stop everything
#
# Idempotent: anything already healthy is left alone, so it is safe to re-run
# after a partial failure or a reboot.
#
# The stack spans two hosts. Dify, Chatwoot, Redis, the portal and the three Go
# services run under WSL; the embedding server runs on Windows, because the
# model weights and the Python runtime that loads them live there. A script
# inside WSL cannot start a Windows process, so this one checks that service and
# tells you how to start it rather than pretending it can.

set -uo pipefail

REPO=/mnt/e/kefu
RUN=/root/unica-run
DIFY_DIR=/data/unica-dify
CHATWOOT_DIR="$REPO/deploy/chatwoot-local"
EMBED_PORT=8199

RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; DIM=$'\033[2m'; RST=$'\033[0m'
ok()   { echo "  ${GRN}OK${RST}   $*"; }
warn() { echo "  ${YEL}WARN${RST} $*"; }
bad()  { echo "  ${RED}DOWN${RST} $*"; }
step() { echo; echo "${DIM}== $* ==${RST}"; }

wsl_ip()      { hostname -I | awk '{print $1}'; }
windows_ip()  { ip route | awk '/^default/ {print $3; exit}'; }

http_ok() { # url -> 0 when it answers at all
  local code
  code=$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$1" 2>/dev/null)
  [ -n "$code" ] && [ "$code" != "000" ]
}

container_up() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

# Liveness is decided by the listening port, not the process name. Linux
# truncates a process name to 15 characters, so "router-scene-bin" appears as
# "router-scene-bi" and `pgrep -x` on the real name silently finds nothing —
# reporting a healthy router as down. The port is also the thing actually worth
# knowing: a process that is up but not listening is not serving anyone.
port_up() { ss -ltn 2>/dev/null | grep -qE "[:.]$1[[:space:]]"; }

# For stopping we still need the PID. -f matches the full command line, which
# keeps the truncation out of it; pgrep excludes itself, so the pattern cannot
# match this very lookup.
pid_of() { pgrep -f -- "$1" 2>/dev/null | head -1; }

# --- start helpers -----------------------------------------------------------

start_dify() {
  if container_up unica-dify-nginx; then ok "Dify 已在运行"; return; fi
  if [ ! -f "$DIFY_DIR/docker-compose.yml" ]; then
    bad "找不到 $DIFY_DIR/docker-compose.yml"; return 1
  fi
  echo "  启动 Dify ..."
  (cd "$DIFY_DIR" && docker compose up -d >/dev/null 2>&1)
  container_up unica-dify-nginx && ok "Dify 已启动" || bad "Dify 启动失败"
}

start_business_redis() {
  if container_up unica-redis; then ok "业务 Redis 已在运行"; return; fi
  echo "  启动业务 Redis ..."
  docker start unica-redis >/dev/null 2>&1 ||
    docker run -d --name unica-redis --restart unless-stopped -p 6380:6379 redis:7-alpine >/dev/null 2>&1
  container_up unica-redis && ok "业务 Redis 已启动" || bad "业务 Redis 启动失败"
}

start_chatwoot() {
  if container_up unica-chatwoot-rails; then ok "Chatwoot 已在运行"; return; fi
  if [ ! -f "$CHATWOOT_DIR/.env" ]; then
    warn "Chatwoot 未配置：先从 .env.example 复制出 $CHATWOOT_DIR/.env"; return
  fi
  echo "  启动 Chatwoot（首次需要跑迁移，会慢）..."
  (cd "$CHATWOOT_DIR" && docker compose up -d >/dev/null 2>&1)
  container_up unica-chatwoot-rails && ok "Chatwoot 已启动" || bad "Chatwoot 启动失败"
}

start_portal() {
  if container_up unica-portal; then ok "门户已在运行"; return; fi
  echo "  启动门户 ..."
  docker start unica-portal >/dev/null 2>&1 || docker run -d \
    --name unica-portal --network host --restart unless-stopped \
    -v "$REPO/portal:/portal:ro" \
    -v "$RUN/portal-nginx/portal.conf:/etc/nginx/conf.d/default.conf:ro" \
    nginx:alpine >/dev/null 2>&1
  container_up unica-portal && ok "门户已启动" || bad "门户启动失败"
}

# Go services are host processes, not containers: they are built on Windows and
# copied in, and running them outside Docker keeps that loop short.
start_go_service() { # name binary envfile port
  local name=$1 bin=$2 envfile=$3 port=$4
  if port_up "$port"; then ok "$name 已在运行 (:$port)"; return; fi
  if [ ! -x "$RUN/$bin" ]; then warn "$name 未部署：$RUN/$bin 不存在"; return; fi
  if [ ! -f "$RUN/$envfile" ]; then warn "$name 缺少环境文件 $RUN/$envfile"; return; fi
  echo "  启动 $name ..."
  ( cd "$RUN" && set -a && . "./$envfile" && set +a && nohup "./$bin" >>"${bin%.*}.log" 2>&1 & )
  for _ in 1 2 3 4 5 6 7 8 9 10; do port_up "$port" && break; sleep 1; done
  port_up "$port" && ok "$name 已启动 (:$port)" || bad "$name 启动失败，看 $RUN/${bin%.*}.log"
}

check_embedding() {
  local host_ip; host_ip=$(windows_ip)
  if http_ok "http://$host_ip:$EMBED_PORT/health"; then
    ok "嵌入服务在运行 (Windows $host_ip:$EMBED_PORT)"
    return
  fi
  bad "嵌入服务没起 —— 知识库检索会全部落空"
  echo "       它跑在 Windows 上，本脚本无法代为启动。另开一个终端执行："
  echo "       ${DIM}cd E:\\kefu\\deploy\\embeddings && python server.py${RST}"
}

# --- report ------------------------------------------------------------------

report() {
  local ip; ip=$(wsl_ip)
  step "访问地址（WSL IP = $ip）"
  echo "  门户        http://$ip:3401/"
  echo "  Dify        http://$ip:3402/"
  echo "  客服工作台  http://$ip:3400/"
  echo "  ${DIM}Windows 侧不能用 localhost，必须用上面这个 IP；它每次重启 WSL 都会变${RST}"

  step "容器"
  for c in unica-dify-nginx unica-dify-api unica-dify-postgres unica-redis \
           unica-portal unica-chatwoot-rails unica-chatwoot-sidekiq unica-chatwoot-postgres; do
    container_up "$c" && ok "$c" || bad "$c"
  done

  step "主机进程"
  port_up 8081 && ok "admin   :8081" || bad "admin   :8081"
  port_up 8090 && ok "router  :8090" || bad "router  :8090"
  port_up 8080 && ok "gateway :8080" || warn "gateway :8080（未部署时可忽略）"

  step "端点自检"
  local ip_l=127.0.0.1
  http_ok "http://$ip_l:3401/healthz" && ok "门户 -> admin 反代通" || bad "门户 -> admin 反代不通"
  http_ok "http://$ip_l:3402/"        && ok "Dify 应答"           || bad "Dify 不应答"
  http_ok "http://$ip_l:3400/"        && ok "Chatwoot 应答"       || bad "Chatwoot 不应答"
  check_embedding

  step "账号"
  echo "  门户超管    rehearsal@unica.local / Rehearsal-2026!"
  echo "  Dify        admin@unica.local     / Rehearsal-Dify-2026!"
  echo "  Chatwoot    admin@unica.local     / Chatwoot-2026!"
  echo "  ${DIM}完整信息见 doc/测试信息.md${RST}"
}

stop_all() {
  step "停止"
  for b in gateway-bin router-scene-bin admin-scene-bin; do
    pid=$(pid_of "$RUN/$b")
    [ -z "$pid" ] && pid=$(pid_of "\./$b")
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null && ok "已停 $b (pid $pid)"
    else echo "  ${DIM}skip $b${RST}"; fi
  done
  docker stop unica-portal >/dev/null 2>&1 && ok "已停 unica-portal"
  (cd "$CHATWOOT_DIR" 2>/dev/null && docker compose stop >/dev/null 2>&1) && ok "已停 Chatwoot"
  echo "  ${DIM}Dify 与业务 Redis 保持运行；要停：cd $DIFY_DIR && docker compose down${RST}"
  echo "  ${DIM}Windows 上的嵌入服务需自行 Ctrl-C${RST}"
}

# --- main --------------------------------------------------------------------

case "${1:-}" in
  --status) report; exit 0 ;;
  --stop)   stop_all; exit 0 ;;
  "" ) ;;
  *) echo "用法: $0 [--status|--stop]"; exit 2 ;;
esac

step "启动容器"
start_dify
start_business_redis
start_chatwoot
start_portal

step "启动 Go 服务"
start_go_service "admin"   admin-scene-bin  admin.env   8081
start_go_service "router"  router-scene-bin router.env  8090
start_go_service "gateway" gateway-bin      gateway.env 8080

report
