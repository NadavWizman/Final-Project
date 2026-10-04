#!/usr/bin/env bash
# run.sh — start, stop and inspect every TradeDesk service with one command.
#
#   ./run.sh start     3 oracle signers, 4 validators, then the gateway
#   ./run.sh stop      stop everything started by this script
#   ./run.sh status    show which services are running
#   ./run.sh logs [s]  follow logs (s = yahoo | nasdaq | cnbc | node0..node3 | gateway)
#   ./run.sh deposit <username> <amount>
#                      credit an account as the custodian (the only signer of
#                      deposits; its key is testnet/custody.key)
#
# Options (environment variables):
#   DJANGO_PORT=8000          port of the web app / gateway
#   ORACLE_MODE=live          live prices from Yahoo, Nasdaq and CNBC (default), or
#   ORACLE_MODE=fixed         offline demo: every source signs FIXED_PRICE
#   FIXED_PRICE=190.00
#
# Run `bash setup.sh` once before the first start.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
RUN="$ROOT/.run"
LOGS="$ROOT/logs"
NET="$ROOT/testnet"
DJANGO_PORT="${DJANGO_PORT:-8000}"
ORACLE_MODE="${ORACLE_MODE:-live}"
FIXED_PRICE="${FIXED_PRICE:-190.00}"
ORACLES=(yahoo nasdaq cnbc)
SERVICES=(yahoo nasdaq cnbc node0 node1 node2 node3 gateway)

port_of() {
    case "$1" in
        yahoo)   echo 8001 ;;  nasdaq) echo 8002 ;;  cnbc) echo 8003 ;;
        node0)   echo 26657 ;; node1)  echo 26667 ;; node2) echo 26677 ;; node3) echo 26687 ;;
        gateway) echo "$DJANGO_PORT" ;;
    esac
}

pid_of()     { [ -f "$RUN/$1.pid" ] && cat "$RUN/$1.pid" || true; }
is_running() { local p; p="$(pid_of "$1")"; [ -n "$p" ] && kill -0 "$p" 2>/dev/null; }
port_busy()  { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }
fail()       { echo "✗ $*" >&2; exit 1; }

wait_for_port() {   # wait_for_port <service> <port>
    for _ in $(seq 1 60); do
        port_busy "$2" && return 0
        is_running "$1" || return 1
        sleep 0.5
    done
    return 1
}

launch() {   # launch <service> <dir> <command...>
    local name="$1" dir="$2"; shift 2
    # exec: the recorded PID is the service itself, and it holds no handle on
    # this script's stdin/stdout (so `./run.sh start | tail` returns).
    (cd "$dir" && exec nohup "$@" >>"$LOGS/$name.log" 2>&1 </dev/null) &
    echo $! >"$RUN/$name.pid"
}

preflight() {
    [ -f "$ROOT/backend/.env" ]          || fail "backend/.env is missing — run: bash setup.sh"
    [ -x "$ROOT/nodes/tradedesk-node" ]  || fail "the node is not built — run: bash setup.sh"
    [ -f "$NET/node0/config/genesis.json" ] || fail "no network in ./testnet — run: bash setup.sh"
    for s in "${SERVICES[@]}"; do
        is_running "$s" && fail "$s is already running — use ./run.sh stop first"
        if port_busy "$(port_of "$s")"; then
            fail "port $(port_of "$s") (needed by $s) is already in use by another program"
        fi
    done
}

start() {
    preflight
    mkdir -p "$RUN" "$LOGS"
    echo "Starting TradeDesk…"

    for i in 0 1 2; do
        local o="${ORACLES[$i]}"
        if [ "$ORACLE_MODE" = "fixed" ]; then
            launch "$o" "$ROOT/oracle_service" python3 oracle_server.py --source fixed --price "$FIXED_PRICE" \
                --name "$o" --key "$NET/oracles/$o.key" --port "$(port_of "$o")"
        else
            launch "$o" "$ROOT/oracle_service" python3 oracle_server.py --source "$o" \
                --key "$NET/oracles/$o.key" --port "$(port_of "$o")"
        fi
    done
    for o in "${ORACLES[@]}"; do
        wait_for_port "$o" "$(port_of "$o")" || fail "oracle $o did not start — see logs/$o.log"
    done
    [ "$ORACLE_MODE" = "fixed" ] && echo "  oracles   yahoo, nasdaq, cnbc — fixed \$$FIXED_PRICE (offline demo)" \
                                 || echo "  oracles   yahoo, nasdaq, cnbc — live prices, each signed"

    for i in 0 1 2 3; do
        launch "node$i" "$ROOT" "$ROOT/nodes/tradedesk-node" start -home "$NET/node$i"
    done
    for i in 0 1 2 3; do
        wait_for_port "node$i" "$(port_of "node$i")" || fail "node$i did not start — see logs/node$i.log"
    done
    echo "  nodes     4 validators (RPC :26657 :26667 :26677 :26687)"

    launch gateway "$ROOT/backend" python3 manage.py runserver "127.0.0.1:$DJANGO_PORT"
    wait_for_port gateway "$DJANGO_PORT" || fail "the gateway did not start — see logs/gateway.log"
    echo "  gateway   http://127.0.0.1:$DJANGO_PORT"

    echo ""
    echo "✓ All services running. Open http://127.0.0.1:$DJANGO_PORT"
    echo "  Logs: ./run.sh logs [yahoo|nasdaq|cnbc|node0..node3|gateway]   Stop: ./run.sh stop"
}

stop() {
    local any=0
    # reverse start order: the gateway first, the oracles last
    for (( i=${#SERVICES[@]}-1; i>=0; i-- )); do
        local s="${SERVICES[$i]}" pid; pid="$(pid_of "$s")"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            pkill -TERM -P "$pid" 2>/dev/null || true   # runserver's child process
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
        if is_running "$s"; then printf "  %-8s running (pid %s)\n" "$s" "$(pid_of "$s")"
        else printf "  %-8s stopped\n" "$s"; fi
    done
}

logs() {
    mkdir -p "$LOGS"
    if [ $# -gt 0 ]; then tail -n 50 -f "$LOGS/$1.log"; else tail -n 20 -f "$LOGS"/*.log; fi
}

deposit() {
    [ $# -eq 2 ] || fail "usage: ./run.sh deposit <username|address> <amount>"
    "$ROOT/nodes/tradedesk-node" deposit -key "$NET/custody.key" -to "$1" -amount "$2"
}

case "${1:-}" in
    start)  start ;;
    deposit) shift; deposit "$@" ;;
    stop)   stop ;;
    status) status ;;
    logs)   shift; logs "$@" ;;
    *)      sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
