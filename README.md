# YexJudge

YexJudge is a small online judge service written in Go. It accepts code submissions, queues them, processes them with background workers, and runs test cases inside reusable Docker sandboxes. It uses PostgreSQL for durable submission storage and supports conventional stdin/stdout submissions in C, C++, and Python, along with metadata-driven C++ and Python Function and Class modes.

## Installation

Requirements: Docker (with Compose v2 for the Compose setup), Go for running the server directly, and PostgreSQL for direct runs. Compose starts its own PostgreSQL service. Docker must be running and accessible to YexJudge.

The Compose stack builds `yexjudge-runtime:latest` automatically from
`docker/runtime/Dockerfile`. If you run the Go server directly instead of with
Compose, build the runtime image manually:

```bash
docker build -t yexjudge-runtime:latest -f docker/runtime/Dockerfile .
```

### Run with Docker Compose

Compose starts YexJudge and PostgreSQL. Before the first startup, generate a strong URL-safe password:

```bash
openssl rand -hex 32
```

Put the generated value in `.env` in the YexJudge project root as:

```text
POSTGRES_PASSWORD=<paste-the-generated-hex-password>
```

Keep `.env` private and set restrictive permissions with `chmod 600 .env`. Compose uses this setting both to initialize PostgreSQL and to build YexJudge's database connection URL. The password must match the password stored in the database; changing `.env` later does not rotate the password in an existing PostgreSQL volume. For an existing volume or a password-authentication error, follow the [PostgreSQL troubleshooting steps](docs/operations.md#troubleshooting).

The Compose worker capacities are set in `docker-compose.yml`: `WORKER_COUNT=4`, `SANDBOX_POOL_SIZE=4`, and `COMPILE_SLOTS=2`. Do not put these in `.env` to configure the Compose stack; Compose does not pass arbitrary `.env` entries into the container. The application receives the values explicitly from the Compose service configuration.

Then prepare the workspace and start the stack:

```bash
mkdir -p /home/pixels/.local/share/yexjudge/workspaces
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
- [Development checks, load testing, and profiling](docs/development.md)
- [Architecture](docs/architecture.md)
- [Future roadmap](docs/plan.md)
