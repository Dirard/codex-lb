FROM node:26-alpine AS web
WORKDIR /src
COPY web/ ./web/
RUN npm --prefix web run install:deps
RUN npm --prefix web run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /src/internal/adapters/webui/dist/ ./internal/adapters/webui/dist/
ARG VERSION=go-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /codex-lb ./cmd/codex-lb

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 codex-lb && mkdir /data && chown codex-lb:codex-lb /data && chmod 0700 /data
COPY --from=build /codex-lb /usr/local/bin/codex-lb
COPY LICENSE /licenses/codex-lb
COPY internal/adapters/upstream/LICENSE.codex-relay /licenses/codex-relay
USER codex-lb
VOLUME /data
EXPOSE 2455
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:2455/health/ready || exit 1
ENTRYPOINT ["/usr/local/bin/codex-lb"]
CMD ["serve", "--data-dir", "/data", "--listen", "0.0.0.0:2455"]
