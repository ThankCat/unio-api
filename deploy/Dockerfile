# syntax=docker/dockerfile:1.7

ARG GO_BUILD_IMAGE=golang:1.26.5-alpine
ARG APP_RUNTIME_IMAGE=alpine:3.22
ARG MIGRATE_TOOL_IMAGE=migrate/migrate:v4.18.3

FROM ${GO_BUILD_IMAGE} AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway-server ./cmd/gateway-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin-server ./cmd/admin-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker-server ./cmd/worker-server

FROM ${APP_RUNTIME_IMAGE} AS runtime-base

ARG IMAGE_VERSION=dev
ARG IMAGE_REVISION=unknown
ARG IMAGE_CREATED=unknown

LABEL org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.created="${IMAGE_CREATED}"

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 65532 unio && \
    adduser -S -D -H -u 65532 -G unio unio && \
    mkdir -p /var/log/unio && \
    chown unio:unio /var/log/unio

WORKDIR /app

USER unio:unio

STOPSIGNAL SIGTERM

FROM runtime-base AS gateway

LABEL org.opencontainers.image.title="Unio Gateway"

COPY --from=builder --chown=unio:unio /out/gateway-server /app/gateway-server

EXPOSE 8520

CMD ["/app/gateway-server"]

FROM runtime-base AS admin

LABEL org.opencontainers.image.title="Unio Admin"

COPY --from=builder --chown=unio:unio /out/admin-server /app/admin-server

EXPOSE 8521

CMD ["/app/admin-server"]

FROM runtime-base AS worker

LABEL org.opencontainers.image.title="Unio Worker"

COPY --from=builder --chown=unio:unio /out/worker-server /app/worker-server

CMD ["/app/worker-server"]

FROM ${MIGRATE_TOOL_IMAGE} AS migration

ARG IMAGE_VERSION=dev
ARG IMAGE_REVISION=unknown
ARG IMAGE_CREATED=unknown

LABEL org.opencontainers.image.title="Unio Migrations" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.created="${IMAGE_CREATED}"

COPY migrations /migrations

ENTRYPOINT ["migrate"]
