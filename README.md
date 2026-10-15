# telemetry-decoder

Decodes Tesla Fleet Telemetry protobuf messages to JSON: a payload decoder for the
[NATS Add-on for Splunk](https://github.com/Bre77/splunk_nats).

Tesla Fleet Telemetry publishes protobuf on NATS subjects
`telemetry.<VIN>.data`, `.alerts`, `.errors` and `.connectivity` (and `.metrics`).
Without a decoder the add-on indexes those payloads as base64. With this decoder
each message becomes a one-line JSON event whose fields Splunk extracts directly:

```
index=nats source="*.data" VehicleSpeed=* | timechart avg(VehicleSpeed) by vin
```

It is a single static binary with no runtime dependencies. The messages are
generated from the protos in
[Teslemetry/tesla-protocol](https://github.com/Teslemetry/tesla-protocol/tree/main/proto/telemetry)
(`proto/telemetry`), which track Tesla's
[fleet-telemetry](https://github.com/teslamotors/fleet-telemetry) protos.

## Install

1. Download the binary for the Splunk host from the
   [latest release](https://github.com/Teslemetry/telemetry-decoder/releases)
   (`telemetry-decoder-linux-amd64`, `-linux-arm64`, `-darwin-amd64` or
   `-darwin-arm64`) and check it against `SHA256SUMS`, or [build it](#build).
2. Place it in the add-on's decoder directory, owned by the Splunk user and
   executable. Its file name there is the decoder's name:

   ```
   sudo install -o splunk -g splunk -m 755 telemetry-decoder-linux-amd64 \
     $SPLUNK_HOME/etc/apps/nats/local/decoders/telemetry-decoder
   ```

3. On a **NATS Subscribe** input for `telemetry.>`, open **Payload decoder** and
   set **Decoder** to `telemetry-decoder`. Leave **Decoded Source Type** at
   `nats:json`, which allows the large events alerts produce.
4. To try it from search first:

   ```
   | natssubscribe subject="telemetry.>" account=<account> decoder=telemetry-decoder | spath
   ```

## Arguments

Set these as the input's **Decoder Arguments** (or `decoder_args=` on
`natssubscribe`):

| Argument | Default | Effect |
|---|---|---|
| `-format flat` | yes | Vehicle data has one top-level field per signal (see below). |
| `-format protojson` | | Vehicle data is the protobuf JSON mapping, the same JSON fleet-telemetry writes with `transmit_decoded_records`: `"data":[{"key":"VehicleSpeed","value":{"floatValue":28.6}}]`. |
| `-proto-names` | | Proto field names (`created_at`, `network_interface`, `is_resend`) instead of the JSON names (`createdAt`, `networkInterface`, `isResend`). |
| `-version` | | Prints the version and the tesla-protocol commit, then exits. |

## Output

Every event is one line of JSON. Field names follow the protobuf JSON mapping,
as fleet-telemetry's own JSON output does (lowerCamel, enums by name, timestamps
in RFC 3339 UTC), and every field is present even when unset (`null`, `false`,
`0`, `""`), so an alert without `endedAt` is visibly still active. The VIN comes
from the message, or from the subject when the message has none.

**Vehicle data** (`telemetry.<VIN>.data`), with the default `-format flat`:

```json
{"vin":"TESTVIN0000000001","createdAt":"2026-10-15T00:13:16.273960575Z","isResend":false,"MilesToArrival":0.962417613193002,"Location":{"latitude":37.4919,"longitude":-121.9447},"Gear":"ShiftStateD","DoorState":{"DriverFront":true,"DriverRear":false,"PassengerFront":false,"PassengerRear":false,"TrunkFront":false,"TrunkRear":false},"invalid":["TpmsPressureFr"]}
```

- Each signal is a field named as in Tesla's `Field` enum, holding its value as
  plain JSON: numbers, strings and booleans as themselves, enums by name, and
  structured values as objects with every field (`Location.latitude`,
  `DoorState.DriverFront`, `ScheduledChargingStartTime.hour`).
- Signals the vehicle reports as invalid (unavailable) are listed in `invalid`
  rather than given a value.
- A signal newer than the decoder's protos is named by its number (`"312": 4.5`),
  and a value type newer than the protos is `null`, so nothing is silently lost.
  Updating the protos (see [Development](#development)) names them.

**Alerts** (`telemetry.<VIN>.alerts`):

```json
{"alerts":[{"name":"CP_a101_wcFoldbackActive","audiences":["Customer","Service"],"startedAt":"2026-10-15T00:14:55.164Z","endedAt":null}],"createdAt":"2026-10-15T00:15:05.180915260Z","vin":"TESTVIN0000000001"}
```

**Errors** (`telemetry.<VIN>.errors`):

```json
{"errors":[{"createdAt":"2026-10-14T23:45:04.404614863Z","name":"unsupported_field","tags":{"field_name":"LifetimeEnergyGainedRegen"},"body":""}],"createdAt":"2026-10-15T00:10:12.617991348Z","vin":"TESTVIN0000000001"}
```

**Connectivity** (`telemetry.<VIN>.connectivity`):

```json
{"vin":"TESTVIN0000000001","connectionId":"00000000-0000-4000-8000-000000000001","status":"CONNECTED","createdAt":"2026-10-15T00:13:22.426862796Z","networkInterface":"wifi"}
```

**Metrics** (`telemetry.<VIN>.metrics`) follow the same mapping:
`{"metrics":[{"name":...,"tags":{...},"value":...}],"createdAt":...,"vin":...}`.

Example searches (with an input indexing into `index=nats`):

```
index=nats source="*.data" | stats latest(BatteryLevel) latest(Odometer) by vin
index=nats source="*.data" Location.latitude=* | table _time vin Location.latitude Location.longitude
index=nats source="*.connectivity" | stats count by status networkInterface
index=nats source="*.alerts" | stats count by alerts{}.name
index=nats source="*.data" invalid{}=* | stats count by invalid{}
```

### When a payload cannot be decoded

The decoder answers with an empty line, so the add-on keeps the payload as
base64 and logs the reason, which the decoder writes to stderr as one line (for
example `telemetry.<VIN>.foo: unknown record type "foo"`). That happens for a
subject whose last token is not a known record type, an empty payload, a payload
that is not valid protobuf for its record type, or a request that is not valid
JSON or base64. The decoder keeps running and answers the next message.

### Protocol and throughput

The decoder implements the add-on's
[decoder protocol](https://github.com/Bre77/splunk_nats#decoder-protocol): one
JSON request per line on stdin, one event per line on stdout (flushed after each
line), diagnostics on stderr, and it exits when stdin closes. It answers in a
few microseconds: on live `telemetry.>` traffic it decodes about 200,000 messages
a second on one core, far beyond the stream's rate (about 110 a second).

## Build

Needs Go (the version in `go.mod`).

```
scripts/build.sh            # static binaries for linux/darwin amd64/arm64 in dist/, with SHA256SUMS
go build -o telemetry-decoder .   # just for this machine
```

Builds are reproducible: the same commit and version give byte-identical
binaries.

## Development

```
go vet ./... && go test ./...                         # unit, golden, protocol and binary tests
go test -run '^$' -bench . ./internal/decoder         # throughput (BenchmarkRun: ns per message)
```

- `internal/protos` is generated, and committed. `scripts/generate.sh`
  regenerates it from tesla-protocol at the pinned commit with pinned `protoc`
  and `protoc-gen-go` (the latter via the `tool` directive in `go.mod`). To move
  to a newer tesla-protocol, run `scripts/generate.sh <commit>`, update the
  pinned commit in the script, refresh the golden files and commit. CI checks that
  the committed code matches the pinned protos.
- `internal/decoder/testdata/golden` holds real payloads captured from live
  telemetry and anonymized by `tools/mkfixtures` (fake VIN, connection IDs,
  locations, names and media), one `<name>.request` per fixture, and the
  expected event for each output option. To refresh them from a capture of
  decoder requests (one JSON line per message), run
  `go run ./tools/mkfixtures < capture.jsonl` and then
  `go test ./internal/decoder -run TestGolden -update`, and review the diff.
- `scripts/e2e-splunk.sh` runs the decoder in a disposable Splunk container with
  the add-on against live NATS traffic and checks that every message is decoded.

## Release

Pushing a `v*` tag runs the release workflow: it tests, builds the binaries with
the tag as their version, and publishes a GitHub release with the binaries and
`SHA256SUMS`.
