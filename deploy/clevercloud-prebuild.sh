#!/bin/sh
# Clever Cloud pre-build hook: installs a pinned, checksum-verified Litestream
# binary in $APP_HOME/bin, where deploy/start.sh looks for it.
#
# Environment variables to set on the Clever Cloud Go application:
#   CC_PRE_BUILD_HOOK=deploy/clevercloud-prebuild.sh
#   CC_GO_PKG=./cmd/google-contacts-mcp      main package (binary lands on PATH)
#   CC_RUN_COMMAND=deploy/start.sh
#   CGO_ENABLED=0
#   PORT                                     set by Clever Cloud; the binary then binds 0.0.0.0:$PORT
#   GCMCP_BASE_URL, GCMCP_GOOGLE_CLIENT_ID, GCMCP_GOOGLE_CLIENT_SECRET, GCMCP_ENCRYPTION_KEYS
#   Litestream, from the linked Cellar add-on (the disk is wiped on every deploy):
#     LITESTREAM_REPLICA_URL=s3://<bucket>/google-contacts-mcp.db
#     LITESTREAM_ENDPOINT=https://<CELLAR_ADDON_HOST>
#     LITESTREAM_ACCESS_KEY_ID=<CELLAR_ADDON_KEY_ID>
#     LITESTREAM_SECRET_ACCESS_KEY=<CELLAR_ADDON_KEY_SECRET>
# Run exactly one instance (scaler min = max = 1): SQLite has a single writer.
set -eu

# LITESTREAM_VERSION is the newest stable release at least two weeks old when pinned.
LITESTREAM_VERSION="0.5.18"
# LITESTREAM_SHA256 is the linux/amd64 tarball digest from the release's checksums.txt.
LITESTREAM_SHA256="d3687b9fd0f2e9426bbd3da828429ff3f1dfc5c9b74d54fdf028e4ee510e7dea"
# LITESTREAM_ASSET is the release asset name; Clever Cloud instances are linux/amd64.
LITESTREAM_ASSET="litestream-${LITESTREAM_VERSION}-linux-x86_64.tar.gz"
LITESTREAM_URL="https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/${LITESTREAM_ASSET}"

: "${APP_HOME:?APP_HOME must be set (Clever Cloud sets it)}"
INSTALL_DIR="$APP_HOME/bin"

TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

curl -fsSL --retry 3 -o "$TMP_DIR/$LITESTREAM_ASSET" "$LITESTREAM_URL"
echo "$LITESTREAM_SHA256  $TMP_DIR/$LITESTREAM_ASSET" | sha256sum -c -
tar -xzf "$TMP_DIR/$LITESTREAM_ASSET" -C "$TMP_DIR" litestream

mkdir -p "$INSTALL_DIR"
install -m 0755 "$TMP_DIR/litestream" "$INSTALL_DIR/litestream"
"$INSTALL_DIR/litestream" version
