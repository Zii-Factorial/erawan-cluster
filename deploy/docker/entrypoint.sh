#!/bin/sh
# Starts HAProxy in master-worker mode in the background, then runs the Go API in
# the foreground. The API writes tenant configs into TENANTS_DIR and triggers
# graceful reloads via HAPROXY_RELOAD_CMD (haproxy-reload.sh).
set -e

TENANTS_DIR="${TENANTS_DIR:-/var/lib/erawan-cluster/haproxy/tenants}"
mkdir -p "$TENANTS_DIR"

# -W: master-worker mode (enables SIGUSR2 hot reload)
# Passing the tenants directory as -f loads every *.cfg in it (sorted), and also
# satisfies the API's check that the running haproxy is loading TENANTS_DIR.
haproxy -W -f /etc/haproxy/haproxy.cfg -f "$TENANTS_DIR" &
echo $! > /run/haproxy-master.pid

exec erawan-cluster
