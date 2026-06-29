#!/usr/bin/env bash
# migrate.sh — thin migration runner for erawan-cluster.
#
# Commands:
#   up                 apply all pending *.up.sql files (default)
#   create <name>      scaffold a new numbered migration pair
#   rollback <name>    apply the *.down.sql for the named migration
#
# Requires DB_CONNECTION to be set (sourced from .envrc by the Makefile).

set -euo pipefail

MIGRATIONS_DIR="$(cd "$(dirname "$0")/../migrations" && pwd)"
DB_URL="${DB_CONNECTION:-}"

if [[ -z "$DB_URL" ]]; then
    echo "error: DB_CONNECTION is not set" >&2
    exit 1
fi

psql_exec() {
    psql "$DB_URL" -v ON_ERROR_STOP=1 -q "$@"
}

psql_query() {
    psql "$DB_URL" -v ON_ERROR_STOP=1 -tA -c "$1"
}

ensure_migrations_table() {
    psql_exec -c "
        CREATE TABLE IF NOT EXISTS erawan_schema_migrations (
            version     integer     PRIMARY KEY,
            applied_at  timestamptz NOT NULL DEFAULT now()
        );
    "
}

# ---------- up ----------
cmd_up() {
    ensure_migrations_table

    shopt -s nullglob
    files=("$MIGRATIONS_DIR"/*.up.sql)
    shopt -u nullglob

    if [[ ${#files[@]} -eq 0 ]]; then
        echo "no migration files found in $MIGRATIONS_DIR"
        exit 0
    fi

    # Sort by filename so they run in numeric order.
    IFS=$'\n' sorted=($(sort <<<"${files[*]}")); unset IFS

    applied=0
    for file in "${sorted[@]}"; do
        name=$(basename "$file")
        # Extract leading digits and strip leading zeros for the integer version.
        raw_ver=$(echo "$name" | grep -oE '^[0-9]+')
        version=$((10#$raw_ver))

        count=$(psql_query "SELECT COUNT(*) FROM erawan_schema_migrations WHERE version = $version;")
        if [[ "$count" -gt 0 ]]; then
            echo "skip  (already applied) $name"
            continue
        fi

        echo "apply $name"
        psql_exec -f "$file"
        # Runner owns version tracking; files may also insert with ON CONFLICT DO NOTHING — that is safe.
        psql_exec -c "INSERT INTO erawan_schema_migrations(version) VALUES ($version) ON CONFLICT DO NOTHING;"
        applied=$((applied + 1))
    done

    if [[ $applied -eq 0 ]]; then
        echo "nothing to apply"
    else
        echo "$applied migration(s) applied"
    fi
}

# ---------- create ----------
cmd_create() {
    local name="${1:-}"
    if [[ -z "$name" ]]; then
        echo "error: migration name is required (make migration TABLE=<name>)" >&2
        exit 1
    fi

    # Next version = highest existing prefix + 1.
    shopt -s nullglob
    existing=("$MIGRATIONS_DIR"/*.up.sql)
    shopt -u nullglob

    max=0
    for f in "${existing[@]}"; do
        raw=$(basename "$f" | grep -oE '^[0-9]+')
        n=$((10#$raw))
        (( n > max )) && max=$n
    done

    next=$(printf "%03d" $((max + 1)))
    up_file="$MIGRATIONS_DIR/${next}_${name}.up.sql"
    down_file="$MIGRATIONS_DIR/${next}_${name}.down.sql"

    cat > "$up_file" <<SQL
-- Migration ${next}: ${name} (up)
-- TODO: add UP migration SQL here.

SQL

    cat > "$down_file" <<SQL
-- Migration ${next}: ${name} (down / rollback)
-- TODO: add DOWN migration SQL here.

SQL

    echo "created $up_file"
    echo "created $down_file"
}

# ---------- rollback ----------
cmd_rollback() {
    local name="${1:-}"
    if [[ -z "$name" ]]; then
        echo "error: migration name is required (make migration ROLLBACK=<name>)" >&2
        exit 1
    fi

    shopt -s nullglob
    matches=("$MIGRATIONS_DIR"/*_"${name}".down.sql)
    shopt -u nullglob

    if [[ ${#matches[@]} -eq 0 ]]; then
        echo "error: no down migration found matching '*_${name}.down.sql'" >&2
        exit 1
    fi

    file="${matches[0]}"
    name_base=$(basename "$file")
    raw_ver=$(echo "$name_base" | grep -oE '^[0-9]+')
    version=$((10#$raw_ver))

    ensure_migrations_table

    count=$(psql_query "SELECT COUNT(*) FROM erawan_schema_migrations WHERE version = $version;")
    if [[ "$count" -eq 0 ]]; then
        echo "warn: migration version $version is not recorded as applied — running rollback anyway"
    fi

    echo "rollback $name_base"
    # Delete the version record BEFORE running the file: the down SQL may drop
    # erawan_schema_migrations itself (e.g. the first migration), which would
    # make a post-run DELETE fail.
    psql_exec -c "DELETE FROM erawan_schema_migrations WHERE version = $version;"
    psql_exec -f "$file"
    echo "rolled back version $version"
}

# ---------- dispatch ----------
subcmd="${1:-up}"
shift || true

case "$subcmd" in
    up)       cmd_up "$@" ;;
    create)   cmd_create "$@" ;;
    rollback) cmd_rollback "$@" ;;
    *)
        echo "error: unknown command '$subcmd'" >&2
        echo "usage: migrate.sh [up|create <name>|rollback <name>]" >&2
        exit 1
        ;;
esac
