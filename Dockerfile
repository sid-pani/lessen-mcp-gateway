# syntax=docker/dockerfile:1
ARG GO_VERSION=1.23.2

FROM golang:${GO_VERSION}-bookworm AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential libsqlite3-dev libpq-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
ARG BUILD_TAGS=""
ARG VERSION=0.1.0-dev
ARG COMMIT=container
ARG BUILD_DATE=unknown
RUN if [ -n "$BUILD_TAGS" ]; then \
      CGO_ENABLED=1 go build -trimpath -tags "$BUILD_TAGS" \
        -ldflags "-s -w -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Version=$VERSION -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Commit=$COMMIT -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Date=$BUILD_DATE" \
        -o /out/mcp-gate ./cmd/mcp-gate; \
    else \
      CGO_ENABLED=1 go build -trimpath \
        -ldflags "-s -w -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Version=$VERSION -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Commit=$COMMIT -X github.com/sid-pani/lessen-mcp-gateway/internal/version.Date=$BUILD_DATE" \
        -o /out/mcp-gate ./cmd/mcp-gate; \
    fi

FROM debian:bookworm-slim AS runtime
ARG BUILD_TAGS=""
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libsqlite3-0 tini \
    && if printf '%s' "$BUILD_TAGS" | grep -Eq '(^|[ ,])postgres([ ,]|$)'; then \
         apt-get install -y --no-install-recommends libpq5; \
       fi \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 mcp \
    && useradd --system --uid 10001 --gid 10001 --home /nonexistent --shell /usr/sbin/nologin mcp \
    && mkdir -p /config /data /usr/share/mcp-gateway \
    && chown -R mcp:mcp /config /data
COPY --from=build /out/mcp-gate /usr/local/bin/mcp-gate
COPY mcp-gateway-config.docker.json /usr/share/mcp-gateway/default-config.json
COPY docker/entrypoint.sh /usr/local/bin/mcp-gateway-entrypoint
RUN chmod 0755 /usr/local/bin/mcp-gate /usr/local/bin/mcp-gateway-entrypoint
USER mcp:mcp
VOLUME ["/config", "/data"]
EXPOSE 4444
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/mcp-gateway-entrypoint"]
CMD ["serve", "--config", "/config/mcp-gateway-config.json"]
