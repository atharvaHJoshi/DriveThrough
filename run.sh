#!/usr/bin/env bash
#
# DriveThrough — local development and operations script.
#
# Wraps the whole lifecycle: environment setup, datastore startup with health
# gating, running the API either natively or fully containerised, and teardown.
#
# Usage: ./run.sh <command> [options]
#        ./run.sh help
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Paths and constants
# ---------------------------------------------------------------------------

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILE="$ROOT_DIR/docker/docker-compose.yml"
ENV_FILE="$ROOT_DIR/.env"
ENV_EXAMPLE="$ROOT_DIR/.env.example"
BUILD_DIR="$ROOT_DIR/build"
BINARY="$BUILD_DIR/drivethrough"

COMPOSE_PROJECT="drivethrough"
PG_CONTAINER="drivethrough-postgres"
REDIS_CONTAINER="drivethrough-redis"
API_CONTAINER="drivethrough-api"

# How long to wait for Postgres/Redis to report healthy, in seconds.
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-90}"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

if [[ -t 1 ]]; then
    C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
    C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'
else
    C_RESET=""; C_BOLD=""; C_DIM=""; C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""
fi

info()    { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok()      { printf '%s  ok%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()    { printf '%swarn%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
err()     { printf '%serror%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
skip()    { printf '%s  -- %s%s\n' "$C_DIM" "$*" "$C_RESET"; }
die()     { err "$*"; exit 1; }

hr()      { printf '%s%s%s\n' "$C_DIM" "------------------------------------------------------------" "$C_RESET"; }

# row <colour> <marker> <label> <value>
# Every status table goes through this so the value column lines up regardless of
# which check produced the row.
row() {
    local color="$1" mark="$2" label="$3" value="$4"
    printf '  %s%s%s  %-10s %s\n' "$color" "$mark" "$C_RESET" "$label" "$value"
}

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------

# Compose is invoked either as the v2 plugin or the legacy hyphenated binary.
# Set COMPOSE_CMD as an array; nothing else should call docker compose directly.
detect_compose() {
    if docker compose version >/dev/null 2>&1; then
        COMPOSE_CMD=(docker compose)
    elif command -v docker-compose >/dev/null 2>&1; then
        COMPOSE_CMD=(docker-compose)
    else
        return 1
    fi
    # --env-file is only meaningful when an env file actually exists. Without it
    # Compose still interpolates from the working directory, but being explicit
    # makes the behaviour identical no matter where the script is invoked from.
    if [[ -f "$ENV_FILE" ]]; then
        COMPOSE_CMD+=(--env-file "$ENV_FILE")
    fi
    COMPOSE_CMD+=(-f "$COMPOSE_FILE")
    return 0
}

require_docker() {
    command -v docker >/dev/null 2>&1 || die "docker not found on PATH. Install Docker Engine."
    docker info >/dev/null 2>&1 || die "docker daemon not reachable. Is Docker running? Do you have permission?"
    detect_compose || die "docker compose not found. Install the v2 plugin or docker-compose."
}

require_go() {
    command -v go >/dev/null 2>&1 || die "go not found on PATH."
}

container_state() {
    docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null || printf 'absent'
}

container_healthy() {
    # PostGIS and Redis both declare healthchecks in the compose file. A running
    # container that has not passed its healthcheck yet is not usable, which is
    # exactly what this gates on.
    [[ "$(docker inspect -f '{{.State.Health.Status}}' "$1" 2>/dev/null || true)" == "healthy" ]]
}

port_open() {
    # Bash's /dev/tcp is the portable-enough way to test a listener without
    # depending on ss, netstat, or lsof being installed.
    (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Environment file
# ---------------------------------------------------------------------------

# Never overwrite an existing .env: it holds real secrets, and regenerating it
# would silently point a working local database at default credentials.
ensure_env() {
    if [[ -f "$ENV_FILE" ]]; then
        ok ".env present"
        return 0
    fi
    [[ -f "$ENV_EXAMPLE" ]] || die ".env.example missing; cannot create .env"
    cp "$ENV_EXAMPLE" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    ok "created .env from .env.example"
    warn "review .env before using production integrations"
}

# Reports which optional integrations are live versus running in fallback mode.
# Payments fall back to a mock gateway and push is disabled when their keys are
# empty, so it is easy to believe a feature is wired when it is not.
env_report() {
    [[ -f "$ENV_FILE" ]] || return 0
    local v
    v="$(grep -E '^SMS_PROVIDER=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
    if [[ "$v" == "log" ]]; then
        row "$C_DIM" "--" "SMS" "log provider — OTPs print to the server log, nothing is sent"
    else
        row "$C_GREEN" "ok" "SMS" "provider: $v"
    fi

    v="$(grep -E '^RAZORPAY_KEY_ID=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
    if [[ -z "$v" ]]; then
        row "$C_DIM" "--" "Payments" "MOCK mode — no gateway key, no real money moves"
    else
        row "$C_GREEN" "ok" "Payments" "Razorpay configured"
    fi

    v="$(grep -E '^FCM_PROJECT_ID=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
    if [[ -z "$v" ]]; then
        row "$C_DIM" "--" "Push" "DISABLED — no FCM project configured"
    else
        row "$C_GREEN" "ok" "Push" "FCM configured"
    fi

    v="$(grep -E '^WORKERS_ENABLED=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
    if [[ "$v" == "false" ]]; then
        row "$C_DIM" "--" "Workers" "disabled"
    else
        row "$C_GREEN" "ok" "Workers" "enabled — 4 background loops"
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Datastores
# ---------------------------------------------------------------------------

# Starts the datastores and blocks until both report healthy, so the API is never
# booted against a Postgres that is still replaying its WAL.
start_deps() {
    require_docker
    info "starting postgres and redis"
    "${COMPOSE_CMD[@]}" up -d postgres redis >/dev/null

    info "waiting for healthchecks (timeout ${HEALTH_TIMEOUT}s)"
    local deadline=$((SECONDS + HEALTH_TIMEOUT))
    while :; do
        if container_healthy "$PG_CONTAINER" && container_healthy "$REDIS_CONTAINER"; then
            ok "postgres and redis are healthy"
            return 0
        fi
        if (( SECONDS >= deadline )); then
            err "datastores did not become healthy within ${HEALTH_TIMEOUT}s"
            printf '  postgres: %s / %s\n' "$(container_state "$PG_CONTAINER")" \
                "$(docker inspect -f '{{.State.Health.Status}}' "$PG_CONTAINER" 2>/dev/null || echo n/a)"
            printf '  redis:    %s / %s\n' "$(container_state "$REDIS_CONTAINER")" \
                "$(docker inspect -f '{{.State.Health.Status}}' "$REDIS_CONTAINER" 2>/dev/null || echo n/a)"
            printf '\n%s  logs: %s ./run.sh logs deps%s\n' "$C_DIM" "$ROOT_DIR/run.sh" "$C_RESET"
            exit 1
        fi
        sleep 2
    done
}

deps_running() {
    container_healthy "$PG_CONTAINER" && container_healthy "$REDIS_CONTAINER"
}

# ---------------------------------------------------------------------------
# Health polling
# ---------------------------------------------------------------------------

wait_ready() {
    local port="$1" deadline=$((SECONDS + ${2:-30}))
    while :; do
        if curl -fsS "http://127.0.0.1:${port}/ready" >/dev/null 2>&1; then
            return 0
        fi
        (( SECONDS >= deadline )) && return 1
        sleep 1
    done
}

port_from_env() {
    local p
    p="$(grep -E '^SERVER_PORT=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
    printf '%s' "${p:-8080}"
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

cmd_setup() {
    ensure_env
    require_docker
    info "creating and starting the datastores"
    start_deps
    hr
    info "next: ./run.sh run   (API on this host)  or   ./run.sh docker  (all in containers)"
}

cmd_deps() {
    ensure_env
    start_deps
    hr
    ok "datastores ready"
    info "postgres on localhost:$(grep -E '^DB_PORT=' "$ENV_FILE" | tail -1 | cut -d= -f2- || echo 5432)"
    info "redis    on localhost:$(grep -E '^REDIS_PORT=' "$ENV_FILE" | tail -1 | cut -d= -f2- || echo 6379)"
    info "stop them with: ./run.sh stop"
}

cmd_run() {
    local build_first=false skip_deps=false
    while (($#)); do
        case "$1" in
            --build)    build_first=true ;;
            --skip-deps) skip_deps=true ;;
            *) die "unknown option for 'run': $1" ;;
        esac
        shift
    done

    ensure_env
    require_go

    if [[ "$skip_deps" == false ]]; then
        if deps_running; then
            ok "datastores already healthy, reusing them"
        else
            start_deps
        fi
    fi

    local port; port="$(port_from_env)"
    if port_open "$port"; then
        die "port $port is already in use. Stop the other listener, or set SERVER_PORT in .env."
    fi

    # Migrations run inside the process when DB_RUN_MIGRATIONS=true, so a fresh
    # volume needs no separate step.
    if grep -qE '^DB_RUN_MIGRATIONS=false' "$ENV_FILE" 2>/dev/null; then
        warn "DB_RUN_MIGRATIONS=false — schema is expected to already exist"
    fi

    hr
    env_report
    hr

    local cmd
    if [[ "$build_first" == true ]]; then
        info "building binary"
        mkdir -p "$BUILD_DIR"
        # -trimpath and stripped symbols keep the local build shaped like the
        # container build, so a local bug reproduces in production.
        (cd "$ROOT_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BINARY" ./cmd/server)
        ok "built $BINARY"
        cmd=("$BINARY")
    else
        cmd=(go run ./cmd/server)
    fi

    info "starting API on http://localhost:${port}"
    printf '  health   curl http://localhost:%s/health\n' "$port"
    printf '  ready    curl http://localhost:%s/ready\n' "$port"
    printf '  login    POST http://localhost:%s/api/v1/auth/send-otp\n' "$port"
    printf '%s  Ctrl-C drains connections gracefully.%s\n' "$C_DIM" "$C_RESET"
    hr

    # Exec so the binary becomes PID 1 of this script and receives SIGINT
    # directly, which is what triggers the server's graceful shutdown path.
    exec "${cmd[@]}"
}

cmd_docker() {
    require_docker
    ensure_env
    info "building the image and starting the full stack"
    "${COMPOSE_CMD[@]}" up --build -d

    local port; port="$(port_from_env)"
    info "waiting for the API to report ready (timeout 120s)"
    if wait_ready "$port" 120; then
        hr
        ok "stack is up on http://localhost:${port}"
        info "logs:    ./run.sh logs api"
        info "stop:    ./run.sh stop"
    else
        warn "the API did not report ready in time; recent logs follow"
        "${COMPOSE_CMD[@]}" logs --tail 40 api
        exit 1
    fi
}

cmd_stop() {
    require_docker
    info "stopping the stack"
    "${COMPOSE_CMD[@]}" stop >/dev/null 2>&1 || true
    ok "containers stopped (data volumes kept)"
}

cmd_clean() {
    require_docker
    warn "this DELETES the postgres and redis volumes — all local data is lost."
    printf 'Type "yes" to continue: '
    read -r reply
    [[ "$reply" == "yes" ]] || { info "aborted"; return 0; }
    "${COMPOSE_CMD[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    ok "containers, volumes, and networks removed"
}

cmd_status() {
    require_docker
    hr
    printf '%scontainers%s\n' "$C_BOLD" "$C_RESET"
    docker ps -a --filter "name=$COMPOSE_PROJECT-" \
        --format '  {{.Names}}   {{.Status}}' 2>/dev/null || skip "none"

    printf '\n%sdatastores%s\n' "$C_BOLD" "$C_RESET"
    if deps_running; then
        row "$C_GREEN" "ok" "postgres" "healthy"
        row "$C_GREEN" "ok" "redis" "healthy"
    else
        row "$C_YELLOW" "??" "datastores" "not ready — run: ./run.sh deps"
    fi

    printf '\n%sapi%s\n' "$C_BOLD" "$C_RESET"
    local port; port="$(port_from_env)"
    local body
    if body="$(curl -fsS "http://127.0.0.1:${port}/ready" 2>/dev/null)"; then
        row "$C_GREEN" "ok" "ready" "http://localhost:${port}  $body"
    elif body="$(curl -fsS "http://127.0.0.1:${port}/health" 2>/dev/null)"; then
        row "$C_YELLOW" "??" "health" "up but not ready  $body"
    else
        row "$C_DIM" "--" "api" "not responding on :${port}"
    fi
    hr
}

cmd_logs() {
    require_docker
    local what="${1:-api}"
    case "$what" in
        api|postgres|redis) ;;
        deps) what="postgres redis" ;;
        *) die "unknown target: $what (use api, postgres, redis, or deps)" ;;
    esac
    # shellcheck disable=SC2086
    "${COMPOSE_CMD[@]}" logs -f $what
}

cmd_test() {
    require_go
    hr
    info "go vet"
    go vet ./... && ok "no findings"
    hr
    info "go test"
    # Integration tests skip themselves when no Postgres is reachable, so this is
    # safe to run with no datastores up.
    go test ./...
    hr
}

cmd_doctor() {
    local problems=0
    hr
    printf '%senvironment%s\n' "$C_BOLD" "$C_RESET"

    if command -v go >/dev/null 2>&1; then
        row "$C_GREEN" "ok" "go" "$(go version | awk '{print $3}')"
    else
        row "$C_RED" "!!" "go" "not found on PATH"; problems=$((problems + 1))
    fi

    if docker info >/dev/null 2>&1; then
        row "$C_GREEN" "ok" "docker" "$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',')"
    else
        row "$C_RED" "!!" "docker" "daemon not reachable"; problems=$((problems + 1))
    fi

    if detect_compose; then
        row "$C_GREEN" "ok" "compose" "available"
    else
        row "$C_RED" "!!" "compose" "not available"; problems=$((problems + 1))
    fi

    if [[ -f "$ENV_FILE" ]]; then
        row "$C_GREEN" "ok" ".env" "present"
    else
        row "$C_YELLOW" "??" ".env" "missing — run: ./run.sh setup"; problems=$((problems + 1))
    fi

    if deps_running; then
        row "$C_GREEN" "ok" "postgres" "healthy"
        row "$C_GREEN" "ok" "redis" "healthy"
    else
        row "$C_YELLOW" "??" "postgres" "not healthy — run: ./run.sh deps"
        row "$C_YELLOW" "??" "redis" "not healthy — run: ./run.sh deps"
    fi

    local port; port="$(port_from_env)"
    if port_open "$port"; then
        row "$C_GREEN" "ok" "port $port" "in use — something is already listening"
    else
        row "$C_DIM" "--" "port $port" "free"
    fi

    hr
    printf '%sintegrations%s\n' "$C_BOLD" "$C_RESET"
    env_report
    hr

    if (( problems > 0 )); then
        err "$problems problem(s) found"
        return 1
    fi
    ok "all clear — run: ./run.sh run"
}

usage() {
    cat <<'EOF'
DriveThrough — local development and operations script

USAGE
  ./run.sh <command>

COMMANDS
  setup     Create .env from .env.example (never overwrites) and start datastores
  deps      Start Postgres and Redis, wait until both report healthy
  run       Run the API on this host (starts datastores if needed)
  docker    Build the image and run the entire stack in containers
  stop      Stop all containers, keeping the data volumes
  clean     Stop containers and DELETE the data volumes (asks to confirm)
  status    Show container, datastore, and API health
  logs      Tail logs: ./run.sh logs [api|postgres|redis|deps]
  test      Run go vet and go test ./...
  doctor    Check prerequisites and report anything missing
  help      Show this message

OPTIONS for 'run'
  --build        Compile a stripped binary first instead of using go run
  --skip-deps    Do not start or check the datastores

ENVIRONMENT
  SERVER_PORT          API port (default 8080)
  HEALTH_TIMEOUT       Seconds to wait for datastore health (default 90)

EXAMPLES
  ./run.sh setup && ./run.sh run      # first-time setup, then run locally
  ./run.sh run --build                # run a production-shaped local build
  ./run.sh docker                     # everything in containers
  ./run.sh logs api                   # tail API logs
  ./run.sh stop                       # shut everything down

NOTES
  Migrations are embedded in the binary and applied at boot when
  DB_RUN_MIGRATIONS=true, so a fresh volume needs no extra step.
  With SMS_PROVIDER=log the login OTP is printed to the server log instead of
  being sent. With an empty RAZORPAY_KEY_ID, payments run against a mock
  gateway and no money moves. With an empty FCM_PROJECT_ID, push is disabled.
  The API drains connections gracefully on Ctrl-C or SIGTERM.
EOF
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

main() {
    local cmd="${1:-help}"
    [[ $# -gt 0 ]] && shift || true

    case "$cmd" in
        setup)  cmd_setup "$@" ;;
        deps)   cmd_deps "$@" ;;
        run)    cmd_run "$@" ;;
        docker) cmd_docker "$@" ;;
        stop)   cmd_stop "$@" ;;
        clean)  cmd_clean "$@" ;;
        status) cmd_status "$@" ;;
        logs)   cmd_logs "$@" ;;
        test)   cmd_test "$@" ;;
        doctor) cmd_doctor "$@" ;;
        help|-h|--help) usage ;;
        *)
            err "unknown command: $cmd"
            printf '\n'
            usage
            exit 1
            ;;
    esac
}

main "$@"
