#!/usr/bin/env bash
#
# 白泽 NAS 一键部署 / 运维（fnOS 等 Linux NAS）
#
# 安装或升级（推荐：一条命令搞定，可重复执行）：
#   curl -fsSL https://baize.wydhd.fun/nas/install.sh | sudo bash
#
# 装完之后就用短命令（脚本会把自己装到 /usr/local/bin/baize-nas）：
#   baize-nas status | logs -f | restart | stop | start | rollback | uninstall
#
# 子命令：
#   deploy (默认)      拉取最新后端二进制 -> 本地构建镜像 -> 启动容器；重复执行即升级
#   status             容器状态 + 健康检查 + 数据目录占用
#   start / stop       启动 / 停止容器
#   restart            重启容器
#   logs [-f] [-n N]   查看日志（默认最后 200 行）
#   rollback           回滚到上一个镜像（上次 deploy 前的版本）
#   uninstall          停容器 + 删镜像 + 清空数据（不可恢复，必须加 --yes）
#   version            显示当前运行的版本
#   help               显示帮助
#
# 常用参数：
#   deploy --version 0.6.0     装指定版本（默认自动选 Gitee / GitHub 最新 Release）
#   deploy --binary <路径>     用本地已编译好的 linux/amd64 二进制（开发调试）
#   uninstall --yes [--keep-data]
#
# 可覆盖的环境变量：BAIZE_HOME / BAIZE_DATA_DIR / BAIZE_PORT / BAIZE_BASE_IMAGE /
#                   BAIZE_TOKEN（健康检查用）/ BAIZE_MIRROR（auto|gitee|github）/
#                   BAIZE_GITHUB_REPO / BAIZE_GITEE_REPO / BAIZE_BIN_DIR
#
set -uo pipefail

SCRIPT_URL="${BAIZE_INSTALL_URL:-https://baize.wydhd.fun/nas/install.sh}"
# 发布仓：国内走 Gitee，国外走 GitHub；BAIZE_MIRROR 可强制指定 auto|gitee|github
GITHUB_REPO="${BAIZE_GITHUB_REPO:-HDKL0802/baize-public}"
GITEE_REPO="${BAIZE_GITEE_REPO:-HDKL0037/baize-public}"
REPO="$GITHUB_REPO"
MIRROR="${BAIZE_MIRROR:-auto}"
BASE_DIR="${BAIZE_HOME:-/vol1/@appdata/baize}"
DATA_DIR="${BAIZE_DATA_DIR:-$BASE_DIR/data}"
BUILD_DIR="$BASE_DIR/build"
COMPOSE_DIR="$BASE_DIR/compose"
ENV_FILE="$COMPOSE_DIR/.env"
BIN_DIR="${BAIZE_BIN_DIR:-/usr/local/bin}"
PORT="${BAIZE_PORT:-8787}"
TOKEN="${BAIZE_TOKEN:-}"
# NAS 自带的 fnOS 镜像源对 library/alpine 返回 401，默认走已验证的 daocloud 源
BASE_IMAGE="${BAIZE_BASE_IMAGE:-docker.m.daocloud.io/library/alpine:3.20}"
IMAGE=baize-backend
CONTAINER=baize-backend

C_RESET='\033[0m'; C_CYAN='\033[36m'; C_GREEN='\033[32m'; C_RED='\033[31m'; C_YELL='\033[33m'
say()  { printf "${C_CYAN}%s${C_RESET}\n" "$*"; }
ok()   { printf "${C_GREEN}%s${C_RESET}\n" "$*"; }
warn() { printf "${C_YELL}%s${C_RESET}\n" "$*"; }
die()  { printf "${C_RED}%s${C_RESET}\n" "$*" >&2; exit 1; }

# ---------------- 权限 ----------------
if [ "$(id -u)" = "0" ]; then
  SUDO=0
elif docker info >/dev/null 2>&1; then
  SUDO=0
elif sudo -n true >/dev/null 2>&1; then
  SUDO=1
else
  die "需要 root 权限：curl -fsSL $SCRIPT_URL | sudo bash"
fi
as_root() { if [ "$SUDO" = "1" ]; then sudo "$@"; else "$@"; fi; }

command -v docker >/dev/null 2>&1 || die "没找到 docker。fnOS 请先在「应用中心」装 Docker。"
if as_root docker compose version >/dev/null 2>&1; then
  COMPOSE_MODE=plugin
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_MODE=standalone
else
  die "没找到 docker compose（插件或独立版都行，请先安装）。"
fi
dc() {
  if [ "$COMPOSE_MODE" = "plugin" ]; then
    as_root docker compose --project-directory "$COMPOSE_DIR" "$@"
  else
    as_root docker-compose --project-directory "$COMPOSE_DIR" "$@"
  fi
}

# ---------------- 工具函数 ----------------
current_image() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n 's/^BAIZE_IMAGE=//p' "$ENV_FILE" | head -n1
}
container_running() {
  [ "$(as_root docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" = "true" ]
}
http_health() {
  if [ -n "$TOKEN" ]; then
    curl -fsS -m 8 -H "X-Baize-Token: $TOKEN" "http://127.0.0.1:$PORT/api/health" 2>/dev/null
  else
    curl -fsS -m 8 "http://127.0.0.1:$PORT/api/health" 2>/dev/null
  fi
}
health_ok() {
  if http_health >/dev/null 2>&1; then return 0; fi
  # 没配令牌时 /api/health 可能要鉴权：退化为「容器在跑」即视为可用
  [ -z "$TOKEN" ] && container_running
}
release_api() { # $1=mirror
  if [ "$1" = "gitee" ]; then
    echo "https://gitee.com/api/v5/repos/$GITEE_REPO/releases/latest"
  else
    echo "https://api.github.com/repos/$GITHUB_REPO/releases/latest"
  fi
}
release_json() { curl -fsSL -m 15 "$(release_api "$1")" 2>/dev/null; }
latest_release() { # $1=mirror
  release_json "$1" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1
}
asset_url() { # $1=mirror $2=资产名（含 .sha256 也算名字的一部分）
  release_json "$1" | grep -o '"browser_download_url": *"[^"]*"' \
    | grep -F "$2" | head -n1 | cut -d'"' -f4
}
pick_mirror() { # auto：先试 Gitee（国内快），取不到再 GitHub
  case "$MIRROR" in
    gitee|github) echo "$MIRROR" ;;
    *) if [ -n "$(latest_release gitee)" ]; then echo gitee; else echo github; fi ;;
  esac
}

write_stack_files() {
  as_root mkdir -p "$BUILD_DIR" "$COMPOSE_DIR" "$DATA_DIR" "$DATA_DIR/bin"
  as_root chown -R 1000:1001 "$DATA_DIR" 2>/dev/null || true
  as_root chmod 775 "$DATA_DIR" "$DATA_DIR/bin" 2>/dev/null || true
  cat > "$BUILD_DIR/entrypoint.sh" <<'ENTRYPOINT'
#!/bin/sh
# 白泽后端容器入口：优先执行 <数据目录>/bin 下的后端二进制。
# 设置页「更新后端」把新版下载成 baize-backend.new，重启容器时在这里提升为正式版本；
# 想回滚到镜像内置版本，删掉 /data/bin/baize-backend 即可。
set -e
DIR=/data/bin
if [ -f "$DIR/baize-backend.new" ]; then
  mv -f "$DIR/baize-backend.new" "$DIR/baize-backend"
  chmod +x "$DIR/baize-backend"
fi
if [ -x "$DIR/baize-backend" ]; then
  exec "$DIR/baize-backend" "$@"
fi
exec /app/baize-backend "$@"
ENTRYPOINT
  cat > "$BUILD_DIR/Dockerfile.nas" <<'DOCKERFILE'
ARG BASE_IMAGE=docker.m.daocloud.io/library/alpine:3.20
FROM ${BASE_IMAGE}
RUN apk add --no-cache ca-certificates tzdata chromium fontconfig font-noto-cjk
RUN fc-cache -f
ENV TZ=Asia/Shanghai BAIZE_IN_CONTAINER=1
WORKDIR /app
COPY baize-backend /app/baize-backend
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/baize-backend /app/entrypoint.sh
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["--addr", "0.0.0.0:8787", "--data", "/data"]
DOCKERFILE
  cat > "$COMPOSE_DIR/docker-compose.yml" <<COMPOSE
services:
  baize-backend:
    image: \${BAIZE_IMAGE:-baize-backend:local}
    container_name: $CONTAINER
    restart: unless-stopped
    user: "1000:1001"
    network_mode: host
    environment:
      - TZ=Asia/Shanghai
      - BAIZE_IN_CONTAINER=1
      - BAIZE_MIRROR=$MIRROR
    volumes:
      - $DATA_DIR:/data
    command: ["--addr", "0.0.0.0:$PORT", "--data", "/data"]
COMPOSE
}

install_cli() {
  as_root mkdir -p "$BIN_DIR" || true
  local dst="$BIN_DIR/baize-nas"
  if [ -f "$0" ] && [ "${0##*/}" != "bash" ] && [ "${0##*/}" != "sh" ]; then
    as_root cp "$0" "$dst"
  else
    curl -fsSL "$SCRIPT_URL" | as_root tee "$dst" >/dev/null
  fi
  as_root chmod +x "$dst" && ok "已安装命令：$dst（之后直接 baize-nas <命令>）"
}

# ---------------- 子命令 ----------------
cmd_deploy() {
  local version="" binary=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --version) version="${2:-}"; shift 2 ;;
      --binary)  binary="${2:-}";  shift 2 ;;
      *) die "未知参数：$1" ;;
    esac
  done

  as_root mkdir -p "$BUILD_DIR" "$COMPOSE_DIR" "$DATA_DIR" "$DATA_DIR/bin"

  # 1) 取版本 & 二进制
  local tag mirror url sha_url
  if [ -n "$binary" ]; then
    [ -f "$binary" ] || die "找不到二进制：$binary"
    version="${version:-local}"
    say "=== 1/6 使用本地二进制 $binary ==="
    as_root cp "$binary" "$BUILD_DIR/baize-backend"
  else
    mirror="$(pick_mirror)"
    say "=== 1/6 拉取最新后端二进制（来源：$mirror） ==="
    tag="$(latest_release "$mirror")"
    [ -n "$tag" ] || die "拿不到最新 Release（Gitee / GitHub 都不通）。也可用 --binary 指定本地二进制。"
    version="${version:-${tag#desktop-v}}"
    url="$(asset_url "$mirror" baize-backend-linux-amd64)"
    [ -n "$url" ] || die "该 Release 还没带后端二进制（baize-backend-linux-amd64），稍后再试或用 --binary"
    as_root curl -fL --retry 3 -o "$BUILD_DIR/baize-backend" "$url" \
      || die "下载失败：$url（可用 --binary 指定本地二进制）"
    sha_url="$(asset_url "$mirror" 'baize-backend-linux-amd64.sha256')"
    if [ -n "$sha_url" ] && curl -fsSL -o /tmp/baize.sha256 "$sha_url" 2>/dev/null; then
      local want got
      want="$(awk '{print $1}' /tmp/baize.sha256)"
      got="$(sha256sum "$BUILD_DIR/baize-backend" | awk '{print $1}')"
      [ "$want" = "$got" ] || die "sha256 校验不通过（期望 $want，实际 $got）"
      ok "sha256 校验通过：$got"
      rm -f /tmp/baize.sha256
    else
      warn "该 Release 没有 .sha256，跳过校验"
    fi
    ok "版本 $version（来源：$mirror）"
  fi

  # 2) 落地 Dockerfile / compose
  say "=== 2/6 写入 Dockerfile 与 compose ==="
  write_stack_files
  ok "$COMPOSE_DIR/docker-compose.yml"

  # 3) 备份当前镜像为 previous（供 rollback）
  say "=== 3/6 备份当前镜像 ==="
  local cur; cur="$(current_image)"
  if [ -n "$cur" ] && [ "$cur" != "$IMAGE:$version" ] && as_root docker image inspect "$cur" >/dev/null 2>&1; then
    as_root docker tag "$cur" "$IMAGE:previous" && ok "已把 $cur 标记为 $IMAGE:previous"
  else
    warn "没有可备份的旧镜像（首次部署？）"
  fi

  # 4) 构建镜像
  say "=== 4/6 在 NAS 本地构建镜像 $IMAGE:$version ==="
  as_root docker build --build-arg "BASE_IMAGE=$BASE_IMAGE" \
    -f "$BUILD_DIR/Dockerfile.nas" -t "$IMAGE:$version" "$BUILD_DIR" \
    || die "镜像构建失败（基础镜像拉不动可试 BAIZE_BASE_IMAGE=...）"

  # 5) 停掉遗留的 systemd 单元 + 启动容器
  say "=== 5/6 启动容器 ==="
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q 'baize-backend'; then
    as_root systemctl stop baize-backend >/dev/null 2>&1 || true
  fi
  printf 'BAIZE_IMAGE=%s\n' "$IMAGE:$version" | as_root tee "$ENV_FILE" >/dev/null
  dc up -d || die "docker compose up 失败，看日志：baize-nas logs"

  # 6) 健康检查
  say "=== 6/6 健康检查（最长 30 秒） ==="
  local i
  for i in $(seq 1 10); do
    if health_ok; then
      ok "健康检查通过"; break
    fi
    sleep 3
    [ "$i" = "10" ] && { warn "健康检查未通过，准备回滚"; cmd_rollback; die "已回滚到上一个镜像"; }
  done

  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q 'baize-backend'; then
    as_root systemctl disable baize-backend >/dev/null 2>&1 || true
  fi
  install_cli
  echo
  ok "部署完成：$IMAGE:$version"
  echo "  访问地址： http://<NAS 局域网 IP>:$PORT"
  echo "  常用命令： baize-nas status | logs -f | restart | rollback | uninstall"
}

cmd_status() {
  local image; image="$(current_image)"
  say "=== 容器 ==="
  as_root docker ps -a --filter "name=$CONTAINER" \
    --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' 2>/dev/null || true
  say "=== 健康检查 ==="
  if health_ok; then ok "OK  http://127.0.0.1:$PORT/api/health"; else warn "未通过（容器可能没起，或令牌不对）"; fi
  say "=== 版本 ==="
  echo "  运行镜像： ${image:-未知}"
  echo "  可回滚：   $(as_root docker image inspect "$IMAGE:previous" >/dev/null 2>&1 && echo "有（$IMAGE:previous）" || echo "无")"
  say "=== 数据目录 ==="
  echo "  $DATA_DIR  $(du -sh "$DATA_DIR" 2>/dev/null | awk '{print $1}')"
}

cmd_start()   { dc up -d && ok "已启动"; }
cmd_stop()    { dc stop && ok "已停止"; }
cmd_restart() { dc restart && ok "已重启（约 3 秒后可用）"; }

cmd_logs() {
  local follow="" tailn=200
  while [ $# -gt 0 ]; do
    case "$1" in
      -f|--follow) follow="-f"; shift ;;
      -n) tailn="${2:-200}"; shift 2 ;;
      *) die "未知参数：$1" ;;
    esac
  done
  as_root docker logs --tail "$tailn" $follow "$CONTAINER"
}

cmd_rollback() {
  if ! as_root docker image inspect "$IMAGE:previous" >/dev/null 2>&1; then
    die "没有可回滚的镜像（$IMAGE:previous 不存在）。先执行一次 deploy 才会有备份。"
  fi
  say "回滚到 $IMAGE:previous ..."
  printf 'BAIZE_IMAGE=%s\n' "$IMAGE:previous" | as_root tee "$ENV_FILE" >/dev/null
  dc up -d || die "回滚启动失败"
  sleep 3
  if health_ok; then ok "回滚成功"; else warn "回滚后健康检查仍未通过，请查看日志：baize-nas logs"; fi
}

cmd_uninstall() {
  local assume=0 keep=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --yes) assume=1; shift ;;
      --keep-data) keep=1; shift ;;
      *) die "未知参数：$1" ;;
    esac
  done
  if [ "$assume" != "1" ]; then
    warn "这会删除白泽在 NAS 上的容器、镜像${keep:+,}和全部数据：$DATA_DIR"
    echo "确认请加 --yes：curl -fsSL $SCRIPT_URL | sudo bash -s -- uninstall --yes"
    exit 1
  fi
  say "=== 停止并删除容器 ==="
  dc down --remove-orphans >/dev/null 2>&1 || true
  as_root docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  say "=== 删除镜像 ==="
  for t in previous local; do as_root docker rmi "$IMAGE:$t" >/dev/null 2>&1 || true; done
  if [ "$keep" = "1" ]; then
    warn "保留数据目录：$DATA_DIR"
  else
    say "=== 清空数据（不可恢复） ==="
    as_root rm -rf "$DATA_DIR"
  fi
  as_root rm -f "$ENV_FILE"
  ok "已取消部署。想重新装：curl -fsSL $SCRIPT_URL | sudo bash"
}

cmd_version() {
  echo "当前镜像：$(current_image)"
  echo "脚本地址：$SCRIPT_URL"
}

usage() {
  sed -n '3,30p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || echo "用法见 https://baize.wydhd.fun"
}

CMD="${1:-deploy}"; [ $# -gt 0 ] && shift
case "$CMD" in
  deploy|update|install) cmd_deploy "$@" ;;
  status|ps)             cmd_status ;;
  start)                 cmd_start ;;
  stop)                  cmd_stop ;;
  restart)               cmd_restart ;;
  logs|log)              cmd_logs "$@" ;;
  rollback)              cmd_rollback ;;
  uninstall|remove|rm)   cmd_uninstall "$@" ;;
  version)               cmd_version ;;
  help|-h|--help)        usage ;;
  *) die "未知命令：$CMD（可用：deploy status start stop restart logs rollback uninstall version）" ;;
esac
