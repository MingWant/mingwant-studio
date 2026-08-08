#!/usr/bin/env bash

set -Eeuo pipefail

REPOSITORY_URL="${REPOSITORY_URL:-}"
REPOSITORY_REF="${REPOSITORY_REF:-main}"
INSTALL_DIR="${INSTALL_DIR:-/opt/mingwant-studio}"
CANVAS_HTTP_PORT="${CANVAS_HTTP_PORT:-3000}"
REQUESTED_COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-}"
MINGWANT_COMPOSE_PROJECT_NAME="${REQUESTED_COMPOSE_PROJECT_NAME:-mingwant-studio}"
COMPOSE_FILE="docker-compose.deploy.yml"
BUILD_COMPOSE_FILE="docker-compose.build.yml"

step() {
    printf '\n==> %s\n' "$1"
}

fail() {
    printf '\n安装失败：%s\n' "$1" >&2
    exit 1
}

require_root() {
    if [[ "${EUID}" -ne 0 ]]; then
        fail "请使用 README 中带 sudo 的一键安装命令"
    fi
    [[ "$(uname -s)" == "Linux" ]] || fail "一键部署脚本仅支持 Linux 服务器"
    [[ -n "$REPOSITORY_URL" ]] || fail "请先通过 REPOSITORY_URL 指定你发布的 MingWant Studio Git 仓库"
    [[ "$CANVAS_HTTP_PORT" =~ ^[0-9]+$ ]] || fail "CANVAS_HTTP_PORT 必须是 1 到 65535 的数字"
    ((CANVAS_HTTP_PORT >= 1 && CANVAS_HTTP_PORT <= 65535)) || fail "CANVAS_HTTP_PORT 必须是 1 到 65535 的数字"
    [[ "$MINGWANT_COMPOSE_PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail "COMPOSE_PROJECT_NAME 只能包含小写字母、数字、连字符和下划线，且必须以字母或数字开头"
}

install_packages() {
    local packages=(ca-certificates curl git openssl)

    if command -v apt-get >/dev/null 2>&1; then
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y "${packages[@]}"
    elif command -v yum >/dev/null 2>&1; then
        yum install -y "${packages[@]}"
    else
        fail "暂不支持当前 Linux 发行版，请先手动安装 Docker、Git、curl 和 OpenSSL"
    fi
}

install_docker() {
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        return
    fi

    step "安装 Docker 和 Docker Compose"
    local installer
    installer="$(mktemp)"
    curl -fsSL https://get.docker.com -o "$installer"
    sh "$installer"
    rm -f "$installer"

    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now docker
    elif command -v service >/dev/null 2>&1; then
        service docker start
    fi

    docker compose version >/dev/null 2>&1 || fail "Docker Compose 安装失败"
}

check_docker_runtime() {
    local engine_info
    if ! engine_info="$(docker info --format '{{.OSType}}|{{.ServerVersion}}' 2>&1)"; then
        fail "Docker CLI 已安装，但 Docker Engine 尚未就绪：${engine_info}"
    fi
    [[ "${engine_info%%|*}" == "linux" ]] || fail "当前 Docker Engine 不是 Linux 容器模式"

    local compose_help
    compose_help="$(docker compose up --help 2>&1)" || fail "无法读取 Docker Compose up 能力，请更新 Docker Compose"
    grep -Eq '(^|[[:space:]])--wait([[:space:]]|$)' <<<"$compose_help" || fail "Docker Compose 版本过旧，不支持 --wait；请更新 Docker Compose"
    grep -Eq '(^|[[:space:]])--wait-timeout([[:space:]]|$)' <<<"$compose_help" || fail "Docker Compose 版本过旧，不支持 --wait-timeout；请更新 Docker Compose"
}

sync_source() {
    step "下载明想 MingWant Studio 源码"
    if [[ -d "$INSTALL_DIR/.git" ]]; then
        cd "$INSTALL_DIR"
        [[ -z "$(git status --porcelain --untracked-files=no)" ]] || fail "$INSTALL_DIR 存在本地代码改动，请先处理后再更新"
        git pull --ff-only origin "$REPOSITORY_REF"
        return
    fi

    if [[ -e "$INSTALL_DIR" && -n "$(find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
        fail "$INSTALL_DIR 已存在且不是本脚本安装的仓库，为避免覆盖文件已停止"
    fi
    mkdir -p "$(dirname "$INSTALL_DIR")"
    git clone --depth 1 --branch "$REPOSITORY_REF" "$REPOSITORY_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
}

prepare_environment() {
    if [[ -f .env ]]; then
        grep -Eq '^POSTGRES_PASSWORD=.+$' .env || fail "现有 .env 缺少 POSTGRES_PASSWORD"
        grep -Eq '^DATABASE_URL=.+$' .env || fail "现有 .env 缺少 DATABASE_URL"
        local configured_project_name
        configured_project_name="$(sed -n 's/^COMPOSE_PROJECT_NAME=//p' .env | tail -n 1)"
        # 环境变量优先级高于 .env，必须在 Compose 启动前拦截意外覆盖。
        if [[ -n "$configured_project_name" ]]; then
            [[ "$configured_project_name" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail "现有 .env 的 COMPOSE_PROJECT_NAME 无效；请先人工核对项目名和数据卷后修正"
            if [[ -n "$REQUESTED_COMPOSE_PROJECT_NAME" && "$REQUESTED_COMPOSE_PROJECT_NAME" != "$configured_project_name" ]]; then
                fail "本次 COMPOSE_PROJECT_NAME 与现有 .env 不一致；为避免切换数据卷，请继续使用原值 $configured_project_name"
            fi
        else
            fail "现有 .env 尚未固定 COMPOSE_PROJECT_NAME；为避免按目录名挂载另一套数据卷，请先人工核对 docker compose config 返回的项目名并把确认后的值写入 .env，再重新运行安装器"
        fi
        local configured_http_port
        configured_http_port="$(sed -n 's/^CANVAS_HTTP_PORT=//p' .env | tail -n 1)"
        if [[ -n "$configured_http_port" ]]; then
            [[ "$configured_http_port" =~ ^[0-9]+$ ]] || fail ".env 中的 CANVAS_HTTP_PORT 无效"
            ((configured_http_port >= 1 && configured_http_port <= 65535)) || fail ".env 中的 CANVAS_HTTP_PORT 无效"
            CANVAS_HTTP_PORT="$configured_http_port"
        fi
        return
    fi

    step "生成 PostgreSQL 随机密码和部署配置"
    local database_password
    database_password="$(openssl rand -hex 32)"
    umask 077
    cat >.env <<EOF
POSTGRES_DB=mingwant_studio
POSTGRES_USER=mingwant_studio
POSTGRES_PASSWORD=${database_password}
DATABASE_URL=postgresql://mingwant_studio:${database_password}@postgres:5432/mingwant_studio?sslmode=disable
COMPOSE_PROJECT_NAME=${MINGWANT_COMPOSE_PROJECT_NAME}
CANVAS_HTTP_PORT=${CANVAS_HTTP_PORT}
CANVAS_REGISTRATION_ENABLED=false
CANVAS_ALLOW_PRIVATE_UPSTREAMS=false
CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS=
CANVAS_CORS_ORIGINS=
CANVAS_SHUTDOWN_DRAIN_TIMEOUT=40m
CANVAS_CONTAINER_STOP_GRACE_PERIOD=45m
EOF
}

start_services() {
    step "从源码构建网页与后端镜像"
    # 标准一键部署已下载完整仓库，直接本地构建可避免公开项目被 GHCR 包可见性阻断。
    docker compose --env-file .env -f "$COMPOSE_FILE" -f "$BUILD_COMPOSE_FILE" build backend web

    step "启动 PostgreSQL、Redis、后端和网页服务"
    docker compose --env-file .env -f "$COMPOSE_FILE" -f "$BUILD_COMPOSE_FILE" up -d --remove-orphans --wait --wait-timeout 600
}

print_result() {
    printf '\n部署完成。\n'
    printf '本机 Web 上游：http://127.0.0.1:%s\n' "$CANVAS_HTTP_PORT"
    printf '公网入口：请由 HTTPS 反向代理转发到上述本机地址\n'
    printf '安装目录：%s\n' "$INSTALL_DIR"
    printf '查看状态：cd %q && docker compose --env-file .env -f %s -f %s ps\n' "$INSTALL_DIR" "$COMPOSE_FILE" "$BUILD_COMPOSE_FILE"
    printf '安全备份：cd %q && sudo env CONFIRM_NO_ACTIVE_TASKS=YES bash ./scripts/backup-server.sh\n' "$INSTALL_DIR"
    printf '\n首次打开后注册的第一个账号会自动成为管理员。公网长期使用前请配置 HTTPS。\n'
}

main() {
    require_root
    step "安装服务器基础工具"
    install_packages
    install_docker
    check_docker_runtime
    sync_source
    prepare_environment
    start_services
    print_result
}

main "$@"
