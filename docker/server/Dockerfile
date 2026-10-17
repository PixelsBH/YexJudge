FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/yexjudge ./cmd/server

FROM docker:cli

# Create a non-root user for the YexJudge server (UID 1001 to match host ubuntu user)
RUN addgroup -g 1001 yexjudge && \
    adduser -u 1001 -G yexjudge -h /app -s /usr/sbin/nologin -D yexjudge

COPY --from=build /out/yexjudge /usr/local/bin/yexjudge
WORKDIR /app

# Run as non-root user
USER 1001:1001

ENTRYPOINT ["/usr/local/bin/yexjudge"]
