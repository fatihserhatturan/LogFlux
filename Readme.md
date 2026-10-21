# 🌊 LogFlux

[![CI](https://github.com/fatihserhatturan/LogFlux/actions/workflows/ci.yml/badge.svg)](https://github.com/fatihserhatturan/LogFlux/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Real-time log aggregation and anomaly detection system written in Go.

LogFlux collects logs from multiple sources and normalizes them into a single
`LogEntry` format. It has no external dependencies — only the Go standard library.

## Features

- **File** — tails a log file continuously (like `tail -f`)
- **Syslog** — receives syslog messages over UDP or TCP
- **HTTP** — accepts JSON logs via `POST`, single or batched

## Requirements

- Go 1.21+
- [golangci-lint](https://golangci-lint.run/) (optional, for `make lint`)
- Docker (optional)

## Quick Start

```bash
git clone https://github.com/fatihserhatturan/LogFlux.git
cd LogFlux
make build
./bin/logflux http :8080
```

## Usage

```bash
logflux file <path>                   # tail a file
logflux syslog <udp|tcp> <address>    # syslog receiver
logflux http <address>                # HTTP receiver
```

Examples:

```bash
./bin/logflux file /var/log/app.log
./bin/logflux syslog udp :5140
./bin/logflux syslog tcp :5140
./bin/logflux http :8080
```

Every received entry is printed to stdout:

```
[1] 2026-10-21T23:40:12+03:00 [ERROR] test-app: Database connection failed
```

Stop with `Ctrl+C`.

## HTTP API

| Method | Path      | Description         |
|--------|-----------|---------------------|
| POST   | `/logs`   | Single log entry    |
| POST   | `/batch`  | Array of log entries|
| GET    | `/health` | Health check        |

Payload (`level` is case-insensitive: `debug`, `info`, `warn`/`warning`,
`error`, `crit`/`critical`; unknown values become `INFO`):

```json
{
  "level": "ERROR",
  "message": "Database connection failed",
  "source": "my-app",
  "fields": { "user_id": 123 }
}
```

```bash
curl -X POST localhost:8080/logs \
  -H 'Content-Type: application/json' \
  -d '{"level":"error","message":"Database connection failed","source":"my-app"}'
# {"id":"3f9c...","status":"accepted"}

curl -X POST localhost:8080/batch \
  -d '[{"level":"info","message":"one"},{"level":"warn","message":"two"}]'
# {"accepted":2,"status":"accepted","total":2}
```

## Test Tools

```bash
go run ./tools/send_http single localhost:8080 ERROR "Something broke"
go run ./tools/send_http batch localhost:8080
go run ./tools/send_http health localhost:8080

go run ./tools/send_syslog udp localhost:5140 "<34>Test message"
go run ./tools/send_syslog tcp localhost:5140 "<34>Test message"
```

## Development

```bash
make build          # build to bin/logflux
make test           # tests with race detector + coverage
make test-coverage  # open HTML coverage report
make bench          # benchmarks
make lint           # golangci-lint
make fmt            # gofmt
make help           # all targets
```

## Docker

```bash
make docker-build
make docker-run     # HTTP mode on :8080

# other modes
docker run --rm -p 5140:5140/udp logflux syslog udp :5140
docker run --rm -v /var/log:/logs:ro logflux file /logs/app.log
```

## Project Structure

```
cmd/collector/            CLI entry point
internal/collector/       Source interface
internal/collector/sources/  File, Syslog and HTTP sources
pkg/models/               LogEntry model and level parsing
tools/                    Helper tools to send test logs
```

## Status

🚧 **Under development** — Phase 1: log collection.

## License

[MIT](LICENSE)
