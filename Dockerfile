# syntax=docker/dockerfile:1
#
# Stages:
#   builder     compiles google-contacts-mcp from source.
#   runtime     shared base: Debian slim, non-root user, /data volume.
#   release     used by goreleaser (--target=release): copies its prebuilt binary.
#   default     last stage, used by a plain `docker build .`.

FROM golang:1.26 AS builder
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
ENV GCMCP_LISTEN=0.0.0.0:8080 \
	GCMCP_DB_PATH=/data/google-contacts-mcp.db
# The database is the only state: mount a persistent volume on /data, or it
# lives only as long as the container.
VOLUME ["/data"]
EXPOSE 8080
WORKDIR /app
USER ${APP_UID}:${APP_UID}
# `docker run <image>` serves; `docker run <image> version` runs another command.
ENTRYPOINT ["google-contacts-mcp"]
CMD ["serve"]

FROM runtime AS release
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/google-contacts-mcp /usr/local/bin/google-contacts-mcp

FROM runtime AS default
COPY --from=builder /out/google-contacts-mcp /usr/local/bin/google-contacts-mcp
