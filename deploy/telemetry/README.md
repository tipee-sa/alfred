# Telemetry agent of the Alfred server

`compose.yaml` runs node-exporter and an OpenTelemetry Collector (`otelcol.yaml`) on
`alfred.tipee.dev`. The collector sends the host's metrics and journal, Alfred's JSON logs
parsed, to telemetry-aggregator (`https://telemetry.tipee.dev`), which stores them in
ClickHouse. Alfred's telemetry is its logs, so all of it is in the journal too; node-exporter's
metrics are what only goes this way. The OTLP receiver on `127.0.0.1:4317`/`4318` takes the
collector's own telemetry, and whatever may export OTLP on the host later. Its starting point is
Linear PF-182.

## Token

The aggregator accepts the tokens of its `otlp-tokens` Nomad variable; alfred has its own.
It lives only in `/opt/telemetry/.env` on the host (root, `600`), next to `HOST_NAME=alfred`,
and never in this repository. To set or replace it, in your own terminal, so that it shows
nowhere:

```bash
ssh -t debian@alfred.tipee.dev "sudo bash -c 'umask 077; mkdir -p /opt/telemetry; read -rsp \"OTLP token: \" t; echo; printf \"HOST_NAME=alfred\nOTLP_TOKEN=%s\n\" \"\$t\" > /opt/telemetry/.env'"
```

Then `just deploy-telemetry`, which recreates the containers. Never run `docker compose config`,
`docker inspect` or `docker exec … env` on the host where the output shows: each prints it.

## Deploy

```bash
just deploy-telemetry
```

It copies both files to `/opt/telemetry` and recreates the containers (a bind-mounted
`otelcol.yaml` that changed is not reloaded otherwise). The journal cursor and the export
queue are in the `telemetry_otelcol-data` volume, so the journal resumes where it stopped.

## Check

- `sudo docker compose -f /opt/telemetry/compose.yaml logs otelcol`: a refused token shows
  there as a 401, and only there.
- In ClickHouse (the telemetry MCP server): `ServiceName = 'alfred'` in `platform.otel_logs`,
  `host.name = 'alfred'` on the `node-exporter` metrics.

## Copied from the platform repository

The journald operators, `transform/journald` and `transform/text-logs` repeat those of the
Nomad nodes' collector (`workload/observability/telemetry-collector/otelcol.yaml`), and the
node-exporter flags and image versions its `telemetry-collector.hcl` and
`workload/observability/main.tf`. Follow them when they change, so that the host's services
are named, parsed and measured as everywhere else.
