FROM docker.io/library/golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/yexjudge ./cmd/server

FROM docker.io/library/docker:cli

COPY --from=build /out/yexjudge /usr/local/bin/yexjudge
RUN mkdir -p /app
WORKDIR /app

ENTRYPOINT ["/usr/local/bin/yexjudge"]
