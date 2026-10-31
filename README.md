# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases in isolated container sandboxes. It uses PostgreSQL for durable submission storage and supports conventional stdin/stdout submissions in C, C++, and Python, along with metadata-driven C++ and Python Function and Class modes. Rootless Podman is the recommended container runtime.

## Installation

Requirements for the recommended deployment: rootless Podman, a Compose provider (`podman-compose` or Docker Compose), and Go plus PostgreSQL only if running the server directly. The Compose stack starts PostgreSQL and builds the YexJudge images. The server uses the Docker-compatible Podman API over a Unix socket; a Docker daemon is not required.

If you run the Go server directly instead of with Compose, build the runtime image with Podman:

```bash
podman build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

### Run with rootless Podman Compose

Copy `.env.example` to `.env`. Set `POSTGRES_PASSWORD` to a strong URL-safe value (generate one with `openssl rand -hex 32`), set `CONTAINER_SOCKET` to `/run/user/<uid>/podman/podman.sock` (replace `<uid>` with the output of `id -u`), and set `WORKSPACE_DIR` to an absolute host directory. Keep `.env` private; changing the password later does not rotate credentials in an existing database volume.

```bash
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
mkdir -p "$HOME/.local/share/yexjudge/workspaces"
realpath "$HOME/.local/share/yexjudge/workspaces"
systemctl --user enable --now podman.socket
```

Put the generated password in `POSTGRES_PASSWORD` in `.env`, set `WORKSPACE_DIR` to the absolute path printed by `realpath`, and set `CONTAINER_SOCKET` using your UID. Then start the stack:

```bash
podman compose up --build -d
``` The workspace is mounted at the same path inside YexJudge so Podman can bind-mount it into compile and runtime containers. Compose worker capacities remain configured in `docker-compose.yml`.

The API is bound to `127.0.0.1:8080`, not public interfaces. See [Operations](docs/operations.md#docker-compose-deployment) for workspace, socket, and reverse-proxy details. Stop the stack with `podman compose down`.

YexJudge’s server image includes the Docker CLI as a client for Podman’s Docker-compatible API. `CONTAINER_SOCKET` points to the host’s rootless Podman socket, which is mounted inside the server container at `/var/run/docker.sock`; the API is not exposed over TCP. Access to this socket grants control over the deployment user’s Podman containers, so keep it private and never mount it into the website container.

### Run the Go server directly

For a direct Go server run, create the PostgreSQL database and configure `DATABASE_URL`. The server also needs the Docker-compatible CLI/API client configured to use the rootless Podman socket.

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
