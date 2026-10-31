# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes. It uses PostgreSQL for durable submission storage and supports conventional stdin/stdout submissions in C, C++, and Python, along with metadata-driven C++ and Python Function and Class modes.

## Installation

Requirements: Docker or rootless Podman with a Compose provider for the Compose setup, Go for running the server directly, and PostgreSQL for direct runs. Compose starts its own PostgreSQL service. The selected container engine must be running and its API socket accessible to YexJudge.

The Compose stack builds `yexjudge-runtime:latest` automatically from
`docker/runtime/Dockerfile`. If you run the Go server directly instead of with
Compose, build the runtime image manually:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

### Run with Compose

Compose starts YexJudge and PostgreSQL. Copy `.env.example` to `.env`, then set a strong `POSTGRES_PASSWORD`, `CONTAINER_SOCKET`, and absolute `WORKSPACE_DIR`. Docker uses `/var/run/docker.sock`; rootless Podman uses `/run/user/<uid>/podman/podman.sock` (replace `<uid>` with `id -u` and enable the Podman user socket). Generate a URL-safe database password with `openssl rand -hex 32`. Keep `.env` private; changing the password later does not rotate credentials in an existing database volume.

```bash
cp .env.example .env
chmod 600 .env
podman compose up --build -d  # or: docker compose up --build -d
```

Before starting, create the absolute path configured as `WORKSPACE_DIR`. Compose worker capacities remain configured in `docker-compose.yml`.

The API is bound to `127.0.0.1:8080`, not public interfaces. See [Operations](docs/operations.md#docker-compose-deployment) for workspace, socket, and reverse-proxy details. Stop the stack with `podman compose down` or `docker compose down`.

### Run the Go server directly

Create the PostgreSQL database, copy the environment template, and configure `DATABASE_URL` in `.env`:

```bash
createdb -U postgres yexjudge
cp .env.example .env
```

Then start the server:

```bash
go run ./cmd/server
```

The server applies its schema and migrations during startup. For more configuration and deployment details, see [Operations](docs/operations.md).

## Documentation

- [Submission API and examples](docs/usage.md)
- [Configuration, deployment, and troubleshooting](docs/operations.md)
- [Development checks and load testing](docs/development.md)
- [Architecture](docs/architecture.md)
- [Future roadmap](docs/plan.md)
