#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

INSTALL_DIR="${INSTALL_DIR:-/opt/mingwant-studio}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.deploy.yml}"
ENV_FILE="${ENV_FILE:-.env}"
BACKUP_ROOT="${BACKUP_ROOT:-${INSTALL_DIR}/backups}"
BACKUP_LABEL="${BACKUP_LABEL:-$(date -u +%Y%m%dT%H%M%SZ)}"
BACKUP_DRAIN_SECONDS="${BACKUP_DRAIN_SECONDS:-5}"
CONFIRM_NO_ACTIVE_TASKS="${CONFIRM_NO_ACTIVE_TASKS:-}"

compose_file_path=""
env_file_path=""
backup_root_path=""
backup_work_dir=""
backup_final_dir=""
backup_lock_dir=""
backup_complete=0
lock_acquired=0
pause_attempted=0
backend_was_running=0
web_was_running=0

step() {
    printf '\n==> %s\n' "$1"
}

fail() {
    printf '\n备份失败：%s\n' "$1" >&2
    exit 1
}

compose() {
    docker compose --env-file "$env_file_path" -f "$compose_file_path" "$@"
}

service_container_id() {
    compose ps --all --quiet "$1" 2>/dev/null | head -n 1
}

service_is_running() {
    local service="$1"
    local container_id
    container_id="$(service_container_id "$service")" || return 1
    [[ -n "$container_id" ]] || return 1
    [[ "$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null)" == "true" ]]
}

query_active_work_counts() {
    printf '%s\n' "SELECT (SELECT COUNT(*) FROM tasks WHERE status IN ('queued', 'running')), (SELECT COUNT(*) FROM billing_orders WHERE status IN ('reserved', 'running'));" |
        compose exec -T postgres sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|"'
}

require_no_active_work() {
    local counts
    local active_tasks
    local active_orders
    if ! counts="$(query_active_work_counts)"; then
        fail "无法从 PostgreSQL 核对活动任务和计费订单，未继续暂停服务"
    fi
    counts="$(printf '%s' "$counts" | tr -d '[:space:]')"
    [[ "$counts" =~ ^([0-9]+)\|([0-9]+)$ ]] || fail "PostgreSQL 返回了无法识别的活动状态统计"
    active_tasks="${BASH_REMATCH[1]}"
    active_orders="${BASH_REMATCH[2]}"
    ((active_tasks == 0)) || fail "数据库仍有 ${active_tasks} 个排队中或运行中的任务；请先处理任务及费用状态"
    ((active_orders == 0)) || fail "数据库仍有 ${active_orders} 个预留中或执行中的系统渠道计费订单；请先核对请求与费用状态"
}

wait_for_healthy() {
    local service="$1"
    local timeout_seconds="$2"
    local container_id
    local state
    local deadline=$((SECONDS + timeout_seconds))

    container_id="$(service_container_id "$service")" || return 1
    [[ -n "$container_id" ]] || return 1
    while ((SECONDS < deadline)); do
        state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{if .State.Running}}running{{else}}stopped{{end}}{{end}}' "$container_id" 2>/dev/null || true)"
        if [[ "$state" == "healthy" || "$state" == "running" ]]; then
            return 0
        fi
        if [[ "$state" == "stopped" ]]; then
            return 1
        fi
        sleep 2
    done
    return 1
}

restore_original_state() {
    local original_status=$?
    local restore_status=0
    trap - EXIT INT TERM
    set +e

    if ((pause_attempted == 1)); then
        if ((backend_was_running == 1)); then
            step "恢复备份前的 Backend 运行状态"
            compose start backend
            if [[ $? -ne 0 ]] || ! wait_for_healthy backend 180; then
                printf '警告：Backend 未能恢复到健康状态，请立即检查容器日志。\n' >&2
                restore_status=1
            fi
        fi
        if ((web_was_running == 1)); then
            step "恢复备份前的 Web 运行状态"
            compose start web
            if [[ $? -ne 0 ]] || ! wait_for_healthy web 120; then
                printf '警告：Web 未能恢复到健康状态，请立即检查容器日志。\n' >&2
                restore_status=1
            fi
        fi
    fi

    if ((backup_complete == 1)); then
        printf '\n备份已完成：%s\n' "$backup_final_dir"
        printf '该目录包含明文部署配置、业务数据和密钥，必须加密转移并限制访问。OSS 对象需要单独备份。\n'
    elif [[ -n "$backup_work_dir" && -d "$backup_work_dir" ]]; then
        printf '\n未完成备份保留在：%s\n' "$backup_work_dir" >&2
        printf '请先检查失败原因；该目录不能作为恢复点，也不要与完整备份混用。\n' >&2
    fi

    if ((lock_acquired == 1)); then
        rmdir "$backup_lock_dir"
        if [[ $? -ne 0 ]]; then
            printf '警告：无法移除备份锁目录 %s，请确认没有其他备份进程后手工处理。\n' "$backup_lock_dir" >&2
            restore_status=1
        fi
    fi

    if ((original_status != 0)); then
        exit "$original_status"
    fi
    if ((restore_status != 0)); then
        exit 1
    fi
    exit 0
}

trap restore_original_state EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

main() {
    [[ "$(uname -s)" == "Linux" ]] || fail "服务器备份脚本仅支持 Linux"
    [[ "${EUID}" -eq 0 ]] || fail "请使用管理部署手册中的 sudo 命令运行，以便完整保留 Backend 数据属主和权限"
    [[ "$CONFIRM_NO_ACTIVE_TASKS" == "YES" ]] || fail "请先在任务中心和供应商后台确认没有活动或状态不确定的任务，再显式设置 CONFIRM_NO_ACTIVE_TASKS=YES"
    [[ "$BACKUP_DRAIN_SECONDS" =~ ^[0-9]+$ ]] || fail "BACKUP_DRAIN_SECONDS 必须是 0 到 300 的整数"
    BACKUP_DRAIN_SECONDS=$((10#$BACKUP_DRAIN_SECONDS))
    ((BACKUP_DRAIN_SECONDS >= 0 && BACKUP_DRAIN_SECONDS <= 300)) || fail "BACKUP_DRAIN_SECONDS 必须是 0 到 300 的整数"
    [[ "$BACKUP_LABEL" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$ ]] || fail "BACKUP_LABEL 只能包含字母、数字、点、下划线和短横线，且不超过 80 个字符"
    [[ "$BACKUP_LABEL" != "." && "$BACKUP_LABEL" != ".." ]] || fail "BACKUP_LABEL 不能是 . 或 .."

    local command_name
    for command_name in docker sha256sum find sort xargs realpath stat; do
        command -v "$command_name" >/dev/null 2>&1 || fail "缺少命令：$command_name"
    done
    docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 不可用"

    [[ -d "$INSTALL_DIR" ]] || fail "安装目录不存在：$INSTALL_DIR"
    cd "$INSTALL_DIR"
    [[ -f "$ENV_FILE" ]] || fail "找不到部署环境文件：$ENV_FILE"
    [[ -f "$COMPOSE_FILE" ]] || fail "找不到部署 Compose 文件：$COMPOSE_FILE"
    env_file_path="$(cd "$(dirname "$ENV_FILE")" && pwd -P)/$(basename "$ENV_FILE")"
    compose_file_path="$(cd "$(dirname "$COMPOSE_FILE")" && pwd -P)/$(basename "$COMPOSE_FILE")"

    compose config --quiet || fail "部署 Compose 配置无效"
    local configured_services
    configured_services="$(compose config --services)"
    grep -Fxq "postgres" <<<"$configured_services" || fail "当前 Compose 没有 postgres 服务；SQLite 请按管理部署手册整体备份数据目录"
    grep -Fxq "backend" <<<"$configured_services" || fail "当前 Compose 没有 backend 服务"
    grep -Fxq "web" <<<"$configured_services" || fail "当前 Compose 没有 web 服务"

    backup_lock_dir="$(pwd -P)/.backup-server.lock"
    if ! mkdir -m 700 "$backup_lock_dir" 2>/dev/null; then
        fail "检测到另一个备份进程或遗留锁：$backup_lock_dir；确认没有备份在运行后再手工移除空锁目录"
    fi
    lock_acquired=1

    local postgres_container_id
    local backend_container_id
    local web_container_id
    postgres_container_id="$(service_container_id postgres)"
    backend_container_id="$(service_container_id backend)"
    web_container_id="$(service_container_id web)"
    [[ -n "$postgres_container_id" ]] || fail "PostgreSQL 容器尚未创建"
    [[ -n "$backend_container_id" ]] || fail "Backend 容器尚未创建，无法读取 /data"
    service_is_running postgres || fail "PostgreSQL 容器未运行，不能生成一致性数据库转储"
    require_no_active_work

    if service_is_running backend; then
        backend_was_running=1
    fi
    if service_is_running web; then
        web_was_running=1
    fi

    backup_root_path="$(realpath -m -- "$BACKUP_ROOT")"
    [[ "$backup_root_path" != "/" ]] || fail "BACKUP_ROOT 不能是文件系统根目录"
    local backend_data_source
    backend_data_source="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$backend_container_id")"
    if [[ -n "$backend_data_source" && -d "$backend_data_source" ]]; then
        backend_data_source="$(cd "$backend_data_source" && pwd -P)"
        [[ "$backup_root_path/" != "$backend_data_source/"* ]] || fail "BACKUP_ROOT 位于 Backend /data 的宿主机来源目录内，会形成递归备份：$backend_data_source"
    fi
    mkdir -p "$backup_root_path"
    backup_root_path="$(cd "$backup_root_path" && pwd -P)"
    backup_work_dir="${backup_root_path}/.${BACKUP_LABEL}.incomplete"
    backup_final_dir="${backup_root_path}/${BACKUP_LABEL}"
    [[ ! -e "$backup_work_dir" ]] || fail "未完成备份目录已存在：$backup_work_dir"
    [[ ! -e "$backup_final_dir" ]] || fail "备份目录已存在：$backup_final_dir"
    mkdir -m 700 "$backup_work_dir"

    step "记录部署版本与服务状态"
    cp -- "$env_file_path" "$backup_work_dir/deployment.env"
    cp -- "$compose_file_path" "$backup_work_dir/docker-compose.deploy.yml"
    if [[ -f "$INSTALL_DIR/docker-compose.build.yml" ]]; then
        cp -- "$INSTALL_DIR/docker-compose.build.yml" "$backup_work_dir/docker-compose.build.yml"
    fi
    compose ps --all >"$backup_work_dir/service-state.txt"

    local git_commit="未记录"
    local git_dirty="unknown"
    local version="未记录"
    if [[ -d "$INSTALL_DIR/.git" ]] && command -v git >/dev/null 2>&1; then
        git_commit="$(git -C "$INSTALL_DIR" rev-parse HEAD 2>/dev/null || printf '读取失败')"
        if [[ -n "$(git -C "$INSTALL_DIR" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
            git_dirty="yes"
        else
            git_dirty="no"
        fi
    fi
    if [[ -f "$INSTALL_DIR/VERSION" ]]; then
        version="$(tr -d '\r\n' <"$INSTALL_DIR/VERSION")"
    fi
    {
        printf 'created_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf 'version=%s\n' "$version"
        printf 'git_commit=%s\n' "$git_commit"
        printf 'git_dirty=%s\n' "$git_dirty"
        printf 'compose_file=%s\n' "$COMPOSE_FILE"
        printf 'backend_was_running=%s\n' "$backend_was_running"
        printf 'web_was_running=%s\n' "$web_was_running"
        printf 'postgres_image=%s\n' "$(docker inspect --format '{{.Config.Image}}@{{.Image}}' "$postgres_container_id")"
        printf 'backend_image=%s\n' "$(docker inspect --format '{{.Config.Image}}@{{.Image}}' "$backend_container_id")"
        if [[ -n "$web_container_id" ]]; then
            printf 'web_image=%s\n' "$(docker inspect --format '{{.Config.Image}}@{{.Image}}' "$web_container_id")"
        else
            printf 'web_image=container-not-created\n'
        fi
        printf 'contains_plaintext_secrets=yes\n'
        printf 'oss_objects_included=no\n'
        printf 'redis_state_included=no\n'
    } >"$backup_work_dir/MANIFEST.txt"

    if ((web_was_running == 1)); then
        step "先暂停 Web，阻止新的公开请求进入"
        pause_attempted=1
        compose stop --timeout 60 web
        if ((BACKUP_DRAIN_SECONDS > 0)); then
            printf '等待 %s 秒，让已进入 Backend 的短请求收敛后再次核对。\n' "$BACKUP_DRAIN_SECONDS"
            sleep "$BACKUP_DRAIN_SECONDS"
        fi
    fi

    # 关闭公开入口后再次核对，缩小“初次检查完成到暂停服务”之间的新任务竞态窗口。
    require_no_active_work
    if ((backend_was_running == 1)); then
        step "暂停 Backend 写入"
        pause_attempted=1
        compose stop --timeout 60 backend
    fi

    step "导出 PostgreSQL"
    compose exec -T postgres sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" "$POSTGRES_DB"' >"$backup_work_dir/postgres.dump"
    [[ -s "$backup_work_dir/postgres.dump" ]] || fail "PostgreSQL 转储为空"
    compose exec -T postgres sh -ec 'pg_restore --list' <"$backup_work_dir/postgres.dump" >"$backup_work_dir/postgres.list"
    [[ -s "$backup_work_dir/postgres.list" ]] || fail "PostgreSQL 转储目录为空"

    step "复制 Backend 数据与 .settings-key"
    backend_container_id="$(service_container_id backend)"
    [[ -n "$backend_container_id" ]] || fail "暂停后找不到 Backend 容器"
    mkdir -m 700 "$backup_work_dir/backend-data"
    docker cp --archive "${backend_container_id}:/data/." "$backup_work_dir/backend-data/"
    [[ -f "$backup_work_dir/backend-data/.settings-key" ]] || fail "Backend 数据中缺少 .settings-key；不能生成无法解密历史密文的恢复点"
    [[ ! -L "$backup_work_dir/backend-data/.settings-key" ]] || fail ".settings-key 不能是符号链接"
    if find "$backup_work_dir/backend-data" -type l -print -quit | grep -q .; then
        fail "Backend 数据包含不受支持的符号链接；请先审计数据目录，避免恢复时越界引用"
    fi
    stat --printf='settings_key_uid=%u\nsettings_key_gid=%g\nsettings_key_mode=%a\n' "$backup_work_dir/backend-data/.settings-key" >"$backup_work_dir/backend-data-settings-key.stat"
    (
        cd "$backup_work_dir"
        find backend-data -printf '%y %m %U %G %p\n' | LC_ALL=C sort >backend-data.metadata
    )

    step "生成并复核 SHA-256 校验清单"
    (
        cd "$backup_work_dir"
        find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum >SHA256SUMS
        sha256sum --check SHA256SUMS >/dev/null
    )
    mv -- "$backup_work_dir" "$backup_final_dir"
    backup_complete=1
    backup_work_dir=""
}

main "$@"
