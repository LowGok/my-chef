FROM oven/bun:1.4.4@sha256:3f23864d3e8c923975c61971cabe67ee3d5a728f1e25a559256b95af719a3478 AS web
WORKDIR /build/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/tsconfig.json web/index.html ./
COPY web/src/ ./src/
COPY web/public/ ./public/
COPY scripts/build-entry.ts /build/scripts/build-entry.ts
RUN bun run build

FROM golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS build
WORKDIR /build
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY migrations/ ./migrations/
COPY web/embed.go ./web/embed.go
COPY --from=web /build/web/dist/ ./web/dist/
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/mychef ./cmd/mychef && \
    go build -trimpath -ldflags="-s -w" -o /out/mychef-migrate ./cmd/mychef-migrate && \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/mychef-import-worker ./cmd/mychef-import-worker && \
    mkdir /runtime && mkdir -m 0700 /runtime/data && \
    mkdir /runtime-worker && mkdir -m 0700 /runtime-worker/import-state /runtime-worker/import-policy && \
    mkdir -m 1770 /runtime-worker/import-channel

FROM gcr.io/distroless/static-debian13:nonroot@sha256:336e53e9941facdb69b755e6fd2c63f5400abd3b1ba47bb17ebafc2b1547cebd AS final
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/mychef /out/mychef-migrate /out/mychef-import-worker /app/
COPY --from=build --chown=65532:65532 /runtime/ /
COPY --from=build --chown=65531:65532 /runtime-worker/ /
ENV MYCHEF_ADDR=0.0.0.0:8080 MYCHEF_DB=/data/mychef.db
COPY LICENSE /app/LICENSE
COPY internal/auth/blocklist/LICENSE /app/THIRD-PARTY-NOTICES.txt
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/mychef", "healthcheck"]
ENTRYPOINT ["/app/mychef"]
