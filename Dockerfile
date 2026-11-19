# syntax=docker/dockerfile:1
#
# Stages:
#   litestream  downloads the pinned, checksum-verified Litestream binary.
#   builder     compiles google-contacts-mcp from source.
#   runtime     shared base: Debian slim, non-root user, Litestream, deploy/.
#   release     used by goreleaser (--target=release): copies its prebuilt binary.
#   default     last stage, used by a plain `docker build .`.

FROM debian:stable-slim AS litestream
ARG TARGETARCH
# LITESTREAM_VERSION is the newest stable release at least two weeks old when
# pinned; keep it equal to the one in deploy/clevercloud-prebuild.sh.
ARG LITESTREAM_VERSION=0.5.18
# Per-arch tarball digests, copied from the release's checksums.txt.
ARG LITESTREAM_SHA256_AMD64=d3687b9fd0f2e9426bbd3da828429ff3f1dfc5c9b74d54fdf028e4ee510e7dea
ARG LITESTREAM_SHA256_ARM64=922a09e6a7ac67f2095571894dc6382e94e1dd4cdd9674883ddf1a8e74ab982d
# Litestream names its assets x86_64/arm64, Docker names them amd64/arm64.
RUN set -eu; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates curl; \
	rm -rf /var/lib/apt/lists/*; \
	case "$TARGETARCH" in \
		amd64) asset_arch=x86_64; sha256="$LITESTREAM_SHA256_AMD64" ;; \
		arm64) asset_arch=arm64; sha256="$LITESTREAM_SHA256_ARM64" ;; \
		*) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
	esac; \
	asset="litestream-${LITESTREAM_VERSION}-linux-${asset_arch}.tar.gz"; \
	curl -fsSL --retry 3 -o "/tmp/$asset" "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/$asset"; \
	echo "$sha256  /tmp/$asset" | sha256sum -c -; \
	tar -xzf "/tmp/$asset" -C /usr/local/bin litestream; \
	/usr/local/bin/litestream version

FROM golang:1.25 AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
	-o /out/google-contacts-mcp ./cmd/google-contacts-mcp

FROM debian:stable-slim AS runtime
# APP_UID is a fixed non-root uid, outside the range distro packages allocate.
ARG APP_UID=10001
RUN set -eu; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates; \
	rm -rf /var/lib/apt/lists/*; \
	useradd --system --uid "$APP_UID" --user-group --no-create-home --home-dir /app app; \
	mkdir -p /data; \
	chown "$APP_UID:$APP_UID" /data
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY deploy/start.sh deploy/litestream.yml /app/deploy/
# No VOLUME: Litestream replication is the persistence story, /data is scratch.
ENV GCMCP_LISTEN=0.0.0.0:8080 \
	GCMCP_DB_PATH=/data/google-contacts-mcp.db
EXPOSE 8080
WORKDIR /app
USER ${APP_UID}:${APP_UID}
ENTRYPOINT ["/app/deploy/start.sh"]

FROM runtime AS release
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/google-contacts-mcp /usr/local/bin/google-contacts-mcp

FROM runtime AS default
COPY --from=builder /out/google-contacts-mcp /usr/local/bin/google-contacts-mcp
