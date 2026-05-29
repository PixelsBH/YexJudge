FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        coreutils \
        openjdk-17-jre-headless \
        python3 \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 10001 judge \
    && useradd --uid 10001 --gid judge --no-create-home --shell /usr/sbin/nologin judge \
    && mkdir -p /workspace /tmp \
    && chown judge:judge /workspace /tmp

USER 10001:10001
WORKDIR /workspace

CMD ["sleep", "infinity"]
