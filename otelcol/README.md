# otelcol-danbot

A minimal OpenTelemetry Collector for the bot instance, built with the
[OpenTelemetry Collector Builder](https://opentelemetry.io/docs/collector/custom-collector/)
(ocb). It contains only what `adot/otel-agent-config.yaml` uses, instead of the
full `otelcol-contrib` distribution (about 420 MB on disk, about 200 MB RSS).

| Kind      | Components                                   |
|-----------|----------------------------------------------|
| receivers | `otlp`, `hostmetrics`, `journald`            |
| processors| `batch`, `memory_limiter`                    |
| exporters | `otlp` (gRPC, to Honeycomb)                  |
| providers | `file`, `env`                                |

## Build

```sh
otelcol/build.sh          # linux/arm64, the instance's architecture
otelcol/build.sh amd64    # for a local test run on x86
```

Output: `otelcol/_build/otelcol-danbot-linux-<arch>` and its `.sha256`.
The script pins ocb and the Go toolchain, and builds with `CGO_ENABLED=0`,
`-trimpath` and no VCS stamp, so a build is reproducible.

Check a config against the binary:

```sh
HONEYCOMB_API_KEY=dummy otelcol/_build/otelcol-danbot-linux-amd64 validate --config=adot/otel-agent-config.yaml
```

## Release

`.github/workflows/otelcol.yml` builds on every pull request that touches this
directory or the collector config, and on `master` uploads to the private
artifacts bucket (`terraform/artifacts.tf`):

```
s3://openai-discord-bot-prod-artifacts/artifacts/otelcol-danbot/<dist.version>/otelcol-danbot-linux-arm64
s3://openai-discord-bot-prod-artifacts/artifacts/otelcol-danbot/<dist.version>/otelcol-danbot-linux-arm64.sha256
```

A version is never overwritten. To ship a change (new components, a collector
upgrade), bump `dist.version` in `builder-config.yaml`; for an upgrade, also
bump every component version and `OCB_VERSION` in `build.sh`. After the upload,
set the version and the sha256 from the workflow summary in terraform and run
the Terraform workflow, which replaces the instance.

## On the instance

`terraform/user-data.sh.tftpl` sets up `otel-collector.service`. Before every
start, `otelcol-install.sh` (as root) makes sure `/usr/local/bin/otelcol-danbot`
is the pinned version: if not, it fetches it with `fetch-artifact`, which
checks the `.sha256` next to the object and the `otelcol_sha256` terraform
variable. So a failed fetch is retried on every restart. The collector runs as `otel-collector.service`
under its own `otelcol` user, with the `systemd-journal` group for the journald
receiver. `otelcol-env.sh` (run as root before each start) puts the Honeycomb
key from SSM into `/run/otelcol/env`. Memory: `GOMEMLIMIT=80MiB`,
`memory_limiter` at 75/100 MiB, `MemoryMax=200M`.

Check it over SSM: `systemctl status otel-collector`,
`journalctl -u otel-collector -n 50`, `ps -o rss= -C otelcol-danbot`.

## Rollback to otelcol-contrib

Revert the PR that switched user-data to this collector (it restores the
otelcol-contrib download, `start-otel.sh` and the `otel_collector_version`
variable), then run the Terraform workflow; the launch template change
triggers an instance refresh. The config stays compatible: otelcol-contrib
0.164.0 accepts the same component names and `memory_limiter`.
