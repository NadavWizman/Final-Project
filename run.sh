#!/usr/bin/env bash
# run.sh — start, stop and inspect every TradeDesk service with one command.
#
#   ./run.sh start     start Oracle, 2 Validators, Leader and Django (in that order)
#   ./run.sh stop      stop everything started by this script
#   ./run.sh status    show which services are running
#   ./run.sh logs [s]  follow the logs (s = oracle | node1 | node2 | node3 | django)
#
# Options (environment variables):
#   DJANGO_PORT=8000          port for the web app / API
#   ORACLE_MODE=live          live Yahoo Finance prices (default), or
#   ORACLE_MODE=fixed         offline demo: every ticker is priced at FIXED_PRICE
#   FIXED_PRICE=190.00        price used when ORACLE_MODE=fixed
#
# Run `bash setup.sh` once before the first start.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
RUN="$ROOT/.run"
LOGS="$ROOT/logs"
DJANGO_PORT="${DJANGO_PORT:-8000}"
ORACLE_MODE="${ORACLE_MODE:-live}"
FIXED_PRICE="${FIXED_PRICE:-190.00}"
SERVICES=(oracle node2 node3 node1 django)

port_of() {
    case "$1" in
        oracle) echo 8001 ;;
        node2)  echo 9002 ;;
        node3)  echo 9003 ;;
        django) echo "$DJANGO_PORT" ;;
        *)      echo "" ;;       # the Leader does not listen on a port
    esac
}

pid_of() { [ -f "$RUN/$1.pid" ] && cat "$RUN/$1.pid" || true; }

is_running() {
    local pid; pid="$(pid_of "$1")"
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

port_busy() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

wait_for_port() {   # wait_for_port <service> <port>
    for _ in $(seq 1 40); do
        port_busy "$2" && return 0
        is_running "$1" || return 1
        sleep 0.5
    done
    return 1
}

fail() { echo "✗ $*" >&2; exit 1; }

preflight() {
    [ -f "$ROOT/backend/.env" ] || fail "backend/.env is missing — run: bash setup.sh"
    [ -f "$ROOT/nodes/.env" ]   || fail "nodes/.env is missing — run: bash setup.sh"
    [ -x "$ROOT/nodes/nodes_bin" ] || fail "nodes/nodes_bin is not built — run: bash setup.sh"
    for s in "${SERVICES[@]}"; do
        is_running "$s" && fail "$s is already running — use ./run.sh stop first"
        local p; p="$(port_of "$s")"
        if [ -n "$p" ] && port_busy "$p"; then
            fail "port $p (needed by $s) is already in use by another program"
        fi
    done
}

launch() {   # launch <service> <dir> <command...>
    local name="$1" dir="$2"; shift 2
    (cd "$dir" && nohup "$@" >>"$LOGS/$name.log" 2>&1 & echo $! >"$RUN/$name.pid")
}

start() {
    preflight
    mkdir -p "$RUN" "$LOGS"
    export DJANGO_URL="http://127.0.0.1:$DJANGO_PORT/api"

    echo "Starting TradeDesk…"
    if [ "$ORACLE_MODE" = "fixed" ]; then
        launch oracle "$ROOT/oracle_service" env ROGUE_PORT=8001 ROGUE_PRICE="$FIXED_PRICE" python3 rogue_oracle.py
        echo "  oracle   fixed price \$$FIXED_PRICE (offline demo mode)"
    else
        launch oracle "$ROOT/oracle_service" python3 oracle_server.py
        echo "  oracle   live prices (Yahoo Finance)"
    fi
    wait_for_port oracle 8001 || fail "the Oracle did not start — see logs/oracle.log"

    launch django "$ROOT/backend" python3 manage.py runserver "127.0.0.1:$DJANGO_PORT"
    wait_for_port django "$DJANGO_PORT" || fail "Django did not start — see logs/django.log"
    echo "  django   http://127.0.0.1:$DJANGO_PORT"

    launch node2 "$ROOT/nodes" env NODE_NAME=node2 ./nodes_bin
    launch node3 "$ROOT/nodes" env NODE_NAME=node3 ./nodes_bin
    wait_for_port node2 9002 || fail "node2 did not start — see logs/node2.log"
    wait_for_port node3 9003 || fail "node3 did not start — see logs/node3.log"
    echo "  node2    validator on :9002"
    echo "  node3    validator on :9003"

    launch node1 "$ROOT/nodes" env NODE_NAME=node1 IS_LEADER=true ./nodes_bin
    sleep 2
    is_running node1 || fail "the Leader did not start — see logs/node1.log"
    echo "  node1    leader"

    echo ""
    echo "✓ All services running. Open http://127.0.0.1:$DJANGO_PORT"
    echo "  Logs: ./run.sh logs [oracle|node1|node2|node3|django]   Stop: ./run.sh stop"
}

stop() {
    local any=0
    # stop in reverse start order: Django and the Leader first, the Oracle last
    for (( i=${#SERVICES[@]}-1; i>=0; i-- )); do
        local s="${SERVICES[$i]}" pid; pid="$(pid_of "$s")"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            # runserver spawns a child process; stop the whole group of children too
            pkill -TERM -P "$pid" 2>/dev/null || true
            kill "$pid" 2>/dev/null || true
            echo "  stopped $s"
            any=1
        fi
        rm -f "$RUN/$s.pid"
    done
    [ "$any" = 1 ] && echo "✓ TradeDesk stopped." || echo "Nothing was running."
}

status() {
    for s in "${SERVICES[@]}"; do
        if is_running "$s"; then
            printf "  %-7s running (pid %s)\n" "$s" "$(pid_of "$s")"
        else
            printf "  %-7s stopped\n" "$s"
        fi
    done
}

logs() {
    mkdir -p "$LOGS"
    if [ $# -gt 0 ]; then
        tail -n 50 -f "$LOGS/$1.log"
    else
        tail -n 20 -f "$LOGS"/*.log
    fi
}

case "${1:-}" in
    start)  start ;;
    stop)   stop ;;
    status) status ;;
    logs)   shift; logs "$@" ;;
    *)      sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
