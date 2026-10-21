# 🌊 LogFlux

[![CI](https://github.com/fatihserhatturan/LogFlux/actions/workflows/ci.yml/badge.svg)](https://github.com/fatihserhatturan/LogFlux/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Real-time log aggregation and anomaly detection in a single Go binary.

LogFlux collects logs from files, syslog and HTTP, parses them into structured
entries, stores them on disk, lets you search and tail them through an API and
a built-in web UI, and alerts you when something unusual happens. It uses only
the Go standard library.

## Features

- **Sources** — tail files and globs (follows logrotate rename and
  copytruncate, resumes from checkpoints), syslog over UDP/TCP (RFC 5424,
  RFC 3164, RFC 6587 framing), JSON over HTTP
- **Parsing** — auto-detects JSON lines, logfmt and plain text (leading
  timestamp + level keyword); joins multi-line stack traces
- **Storage** — append-only JSONL segments, gzip-compressed when closed,
  retention by age and size, crash recovery
- **Query** — filter by time, source, level and text; live tail over
  Server-Sent Events; per-minute stats
- **Anomaly detection**
  - *New patterns* — log templates are mined with
    [Drain](http://jiemingzhu.github.io/pub/pjhe_icws2017.pdf); a WARNING+
    template never seen before raises an alert
  - *Spikes* — per-minute error counts far above the recent median
    (modified z-score on median/MAD, EWMA baseline)
  - *Silence* — a source that normally sends logs goes quiet
  - *Rules* — "≥ N matching entries within a window", grouped by a field
- **Alerts** — stdout and webhooks (generic JSON or Slack), duplicate
  suppression with cooldown
- **Web UI** — search, chart, live tail, alerts and templates at `/`
- **Metrics** — Prometheus text format at `/metrics`

## Quick Start

```bash
git clone https://github.com/fatihserhatturan/LogFlux.git
cd LogFlux
make build
./bin/logflux http :8080
```

Send a log and open the UI at <http://localhost:9090>:

```bash
curl -X POST localhost:8080/logs -d '{"level":"error","message":"payment failed","source":"checkout"}'
```

## Usage

```bash
logflux [flags] -config <file.json>        # full configuration
logflux [flags] file <path-or-glob>        # quick single-source modes
logflux [flags] syslog <udp|tcp> <address>
logflux [flags] http <address>
```

| Flag | Description |
|------|-------------|
| `-config` | Path to the JSON config file |
| `-api` | API/UI listen address (default `:9090`) |
| `-data` | Data directory (default `./data`) |
| `-quiet` | Do not print entries to stdout |

## Configuration

See [`logflux.example.json`](logflux.example.json). All sections except
`sources` are optional.

```json
{
  "data_dir": "./data",
  "api": { "addr": ":9090" },
  "sources": [
    { "type": "file", "name": "app", "path": "/var/log/app/*.log", "parser": "auto", "multiline": true },
    { "type": "syslog", "name": "syslog", "protocol": "udp", "addr": ":5140" },
    { "type": "http", "name": "ingest", "addr": ":8080" }
  ],
  "storage": { "retention_max_age": "168h", "retention_max_size_mb": 10240 },
  "detect": {
    "warmup": "1h",
    "rules": [
      { "name": "ssh-bruteforce", "q": "failed password", "window": "5m", "threshold": 10, "group_by": "host" }
    ]
  },
  "notify": [
    { "type": "stdout" },
    { "type": "webhook", "url": "https://hooks.slack.com/services/...", "format": "slack" }
  ]
}
```

<details>
<summary>All options</summary>

| Key | Default | Description |
|-----|---------|-------------|
| `data_dir` | `./data` | Segments, checkpoints, templates and alerts |
| `buffer_size` | `10000` | Main queue size |
| `api.addr` | `:9090` | API and UI address |
| **sources[]** | | |
| `type` | — | `file`, `syslog` or `http` |
| `name` | `<type>-<n>` | Unique name, used as the entry source |
| `path` | — | File path or glob (`file`) |
| `protocol` | `udp` | `udp` or `tcp` (`syslog`) |
| `addr` | — | Listen address (`syslog`, `http`) |
| `parser` | `auto` for files, else `none` | `auto`, `json`, `logfmt`, `plain`, `none` |
| `multiline` | `false` | Join stack-trace lines (`file`) |
| **storage** | | |
| `disabled` | `false` | Do not store entries |
| `segment_max_size_mb` | `64` | Rotate segment after this size |
| `segment_max_age` | `1h` | Rotate segment after this age |
| `retention_max_age` | `168h` | Delete older segments |
| `retention_max_size_mb` | `0` (no limit) | Delete oldest segments above this size |
| `flush_interval` | `1s` | Flush and fsync interval |
| **detect** | | |
| `disabled` | `false` | Turn off all detectors |
| `warmup` | `1h` | No new-pattern alerts until templates are learned |
| `cooldown` | `10m` | Suppress repeats of the same alert |
| `new_pattern.min_level` | `warning` | Minimum level for new-pattern alerts |
| `spike.levels` | `["error","critical"]` | Levels watched for spikes |
| `spike.threshold` | `3.5` | Modified z-score needed |
| `spike.min_count` | `10` | Minimum entries per minute to alert |
| `spike.history` / `min_history` | `60` / `10` | Minutes of history kept / needed |
| `silence.after` | `5m` | Quiet time before alerting |
| `silence.min_rate` | `1` | Entries per minute a source must normally send |
| **detect.rules[]** | | |
| `name`, `window`, `threshold` | — | Required |
| `q`, `sources`, `levels`, `min_level`, `fields` | | Entry filter |
| `group_by` | | `source`, `level` or a field name (e.g. `host`) |
| `severity` | `warning` | Alert severity |
| **notify[]** | stdout | `{"type":"stdout"}` or `{"type":"webhook","url":"…","format":"json\|slack"}` |

</details>

## HTTP Ingest

| Method | Path | Description |
|--------|------|-------------|
| POST | `/logs` | Single entry |
| POST | `/batch` | Array of entries |
| GET | `/health` | Health check |

```json
{ "level": "error", "message": "Database connection failed", "source": "my-app", "fields": { "user_id": 123 } }
```

`level` is case-insensitive (`debug`, `info`, `warn`, `error`, `critical`,
plus aliases like `fatal`, `trace`, `notice`).

## API

All endpoints are `GET` on the API address (default `:9090`).

| Path | Description |
|------|-------------|
| `/` | Web UI |
| `/api/query` | Search stored entries, newest first |
| `/api/tail` | Live entries as Server-Sent Events |
| `/api/stats` | Per-minute counts by source and level |
| `/api/alerts` | Recent alerts (`since`, `kind`, `limit`) |
| `/api/templates` | Mined templates (`sort=count\|recent\|new`, `min_level`, `source`, `q`) |
| `/api/segments` | Storage segments |
| `/metrics` | Prometheus metrics |
| `/health` | Health and uptime |

Query parameters for `/api/query` and `/api/tail`:

| Parameter | Example | Description |
|-----------|---------|-------------|
| `since`, `until` | `15m`, `2026-10-21T10:00:00Z` | Duration ago, RFC 3339 or unix seconds |
| `source` | `api,db` | Comma-separated sources |
| `level` | `error,critical` | Exact levels |
| `min_level` | `warning` | Minimum level |
| `q` | `timeout` | Case-insensitive text in the message |
| `field.<name>` | `field.template_id=12` | Exact field match |
| `limit` | `500` | Max entries (default 100, max 10000) |
| `backlog` | `50` | `/api/tail` only: recent entries sent first |

```bash
curl 'localhost:9090/api/query?since=1h&min_level=error&q=timeout'
curl -N 'localhost:9090/api/tail?source=api&min_level=warning'
curl 'localhost:9090/api/templates?min_level=error&sort=new'
```

## How Detection Works

1. Every entry is assigned a **template** by the Drain algorithm: numbers,
   IPs, UUIDs, dates and similar values are masked, then similar lines are
   grouped (`Connection to <*> timed out after <NUM>`). The template id is
   stored with the entry (`field.template_id`).
2. After the **warm-up**, the first WARNING+ entry of an unseen template
   raises a `new_pattern` alert. Templates are saved to
   `data_dir/templates.json`, so a restart does not re-learn them.
3. Every minute, each `(source, level)` count is compared with the last
   hour: an alert fires when its modified z-score
   (`0.6745 · (x − median) / MAD`) exceeds the threshold. Median and MAD
   are not inflated by the spike itself, unlike mean and standard deviation.
4. A source that averaged at least `min_rate` entries per minute and then
   sent nothing for `silence.after` raises a `silence` alert.
5. Rules count matching entries in a sliding window per `group_by` value.

## Data Directory

```
data/
  segments/*.jsonl(.gz)   stored entries
  segments/index.json     per-segment summaries
  checkpoints.json        file read offsets
  templates.json          learned templates
  alerts.jsonl            alert history
```

## Development

```bash
make build          # build to bin/logflux
make test           # tests with race detector + coverage
make lint           # golangci-lint
make fmt            # gofmt
make help           # all targets
```

Test tools:

```bash
go run ./tools/send_http single localhost:8080 ERROR "Something broke"
go run ./tools/send_syslog udp localhost:5140 "<11>1 2026-10-21T10:00:00Z host app - - - disk failure"
```

## Docker

```bash
make docker-build
make docker-run     # HTTP ingest on :8080, UI on :9090, data in a volume

docker run --rm -p 9090:9090 -p 5140:5140/udp -v logflux-data:/data logflux syslog udp :5140
docker run --rm -p 9090:9090 -v /var/log:/logs:ro -v logflux-data:/data logflux file '/logs/*.log'
docker run --rm -p 9090:9090 -v $PWD/logflux.json:/logflux.json:ro -v logflux-data:/data logflux -config /logflux.json
```

The image runs as a non-root user, so listen on ports above 1024 inside the
container (e.g. `5140` for syslog).

## Project Structure

```
cmd/collector/               CLI entry point
internal/app/                Wires everything from the config
internal/config/             Config loading and validation
internal/collector/sources/  File, syslog and HTTP sources
internal/parser/             Syslog, JSON, logfmt and plain-text parsing
internal/pipeline/           Sources -> processors -> sinks
internal/storage/            Segment store, live tail hub
internal/stats/              Per-minute counters
internal/detect/             Drain templates and anomaly detectors
internal/alert/              Alert log and dispatch
internal/notify/             Stdout and webhook notifiers
internal/api/                HTTP API
internal/ui/                 Embedded web UI
internal/metrics/            Prometheus-format metrics
pkg/models/                  LogEntry and levels
tools/                       Helper tools to send test logs
```

## Limitations

- Single node: no clustering or replication.
- No authentication or TLS on the API: run it on a trusted network or
  behind a reverse proxy.
- Text search scans the candidate segments (no full-text index), so its
  cost grows with the data in the queried time range. Narrow queries with
  `since`, `source` or `level` to skip segments.
- `alerts.jsonl` is append-only and not rotated (alerts are rare, but it
  grows without bound).
- Spike and silence baselines live in memory and are re-learned after a
  restart.

## License

[MIT](LICENSE)
