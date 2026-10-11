# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes. It uses PostgreSQL for durable submission storage and supports conventional stdin/stdout submissions in C, C++, and Python, along with metadata-driven C++ and Python Function and Class modes.

## Installation

Requirements: Docker (with Compose v2 for the Compose setup), Go for running the server directly, and PostgreSQL for direct runs. Compose starts its own PostgreSQL service. Docker must be running and accessible to YexJudge.

Build the runtime image from the project root (required for either setup):

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

### Run with Docker Compose

Compose starts YexJudge and PostgreSQL. Set a strong, URL-safe PostgreSQL password in the ignored `.env` file before starting the stack (see [Compose deployment](docs/operations.md#docker-compose-deployment)):

```bash
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 32)" >> .env
chmod 600 .env
mkdir -p .yexjudge-workspaces
docker compose up --build
```

The API is available to processes on the VPS at `http://127.0.0.1:8080`; it is not published on public interfaces. Stop the stack with `docker compose down`; Compose gives the server time to remove its runtime sandbox containers during shutdown.

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
