# Tag default is for development; release builds must supply a reviewed digest.
ARG RUNTIME_BASE_IMAGE=debian:bookworm-slim
FROM ${RUNTIME_BASE_IMAGE}

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        coreutils \
        time \
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
