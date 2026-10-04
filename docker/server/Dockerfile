# Tag defaults are for local builds only; release builds must supply reviewed digests.
ARG GO_IMAGE=golang:1.25-bookworm
ARG SERVER_BASE_IMAGE=docker:cli
FROM ${GO_IMAGE} AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY db ./db
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/yexjudge ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/yexjudge-migrate ./cmd/migrate

FROM ${SERVER_BASE_IMAGE}

RUN apk add --no-cache ca-certificates curl jq \
    && addgroup -g 10001 judge \
    && adduser -D -H -u 10001 -G judge -s /sbin/nologin judge \
    && mkdir -p /app
COPY --from=build /out/yexjudge /out/yexjudge-migrate /usr/local/bin/
COPY docker/server/readiness.sh /usr/local/bin/yexjudge-readiness
USER 10001:10001
WORKDIR /app

ENTRYPOINT ["/usr/local/bin/yexjudge"]
