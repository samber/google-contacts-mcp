#!/bin/sh
# Starts google-contacts-mcp, wrapped by Litestream when a replica is configured.
#
# With LITESTREAM_REPLICA_URL set, the database is restored from the replica
# when the local file is missing (fresh container or Clever Cloud deploy, whose
# disk is wiped), then Litestream runs the server as a child process and
# replicates every write. Without it, the server runs alone on local disk.
# Any arguments are run as a command instead (e.g. `google-contacts-mcp version`
# in a container whose entrypoint is this script).
set -eu

# SCRIPT_DIR locates litestream.yml whatever the working directory.
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
LITESTREAM_CONFIG="$SCRIPT_DIR/litestream.yml"

# The Clever Cloud pre-build hook installs Litestream in $APP_HOME/bin.
if [ -n "${APP_HOME:-}" ] && [ -d "$APP_HOME/bin" ]; then
	PATH="$APP_HOME/bin:$PATH"
	export PATH
fi

# Same default as the binary's --db-path; exported so litestream.yml and the
# server agree on one file.
GCMCP_DB_PATH="${GCMCP_DB_PATH:-./data/google-contacts-mcp.db}"
export GCMCP_DB_PATH
mkdir -p "$(dirname -- "$GCMCP_DB_PATH")"

if [ "$#" -gt 0 ]; then
	exec "$@"
fi

if [ -n "${LITESTREAM_REPLICA_URL:-}" ]; then
	litestream restore -if-db-not-exists -if-replica-exists -config "$LITESTREAM_CONFIG" "$GCMCP_DB_PATH"
	exec litestream replicate -config "$LITESTREAM_CONFIG" -exec "google-contacts-mcp serve"
fi

exec google-contacts-mcp serve
