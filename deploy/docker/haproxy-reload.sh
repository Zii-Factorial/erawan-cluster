#!/bin/sh
# Graceful HAProxy reload used as HAPROXY_RELOAD_CMD.
# Sending SIGUSR2 to the master process makes it re-read its config (including all
# files in TENANTS_DIR) and spawn new workers without dropping active connections.
set -e

PID_FILE=/run/haproxy-master.pid
if [ ! -f "$PID_FILE" ]; then
    echo "haproxy master pid file not found at $PID_FILE" >&2
    exit 1
fi

kill -USR2 "$(cat "$PID_FILE")"
