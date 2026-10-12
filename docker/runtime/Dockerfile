FROM golang:1.26-bookworm AS runner-build

WORKDIR /src
COPY cmd/yexsupervisor/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/yexsupervisor ./main.go

FROM debian:bookworm-slim

COPY --from=runner-build /out/yexsupervisor /usr/local/bin/yexsupervisor

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        coreutils \
        time \
        python3 \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 10001 judge \
    && useradd --uid 10001 --gid judge --no-create-home --shell /usr/sbin/nologin judge \
    && mkdir -p /workspace /tmp \
    && chown judge:judge /workspace /tmp

USER 10001:10001
WORKDIR /workspace

CMD ["sleep", "infinity"]
