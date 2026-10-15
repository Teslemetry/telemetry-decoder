#!/usr/bin/env bash
# End-to-end check of telemetry-decoder in a disposable Splunk container with the
# NATS Add-on for Splunk, against live Tesla Fleet Telemetry on NATS.
#
#   NATS_URL=nats://host.docker.internal:4222 NATS_USERNAME=... NATS_PASSWORD=... \
#     scripts/e2e-splunk.sh <nats-x.y.z.tar.gz> <telemetry-decoder linux binary> [splunk image] [seconds]
#
# Installs the add-on and the decoder (as local/decoders/telemetry-decoder), runs
# a natssubscribe search with decoder=telemetry-decoder, then indexes
# telemetry.> with a NATS Subscribe input for the given seconds (default 300) and
# checks that every message was decoded: no base64 fallback events and no
# "failed to decode" in splunkd.log. Prints decoded fields per record type,
# aggregated so no VIN or location is printed.
#
# The NATS credentials reach Splunk's REST API through curl's stdin, never argv
# or disk. The container and its volumes are always removed.
set -euo pipefail

package=$(realpath "${1:?usage: $0 <nats-x.y.z.tar.gz> <decoder binary> [splunk image] [seconds]}")
decoder=$(realpath "${2:?decoder binary required}")
image=${3:-splunk/splunk:latest}
seconds=${4:-300}
: "${NATS_URL:?}" "${NATS_USERNAME:?}" "${NATS_PASSWORD:?}"

name=telemetry-decoder-e2e-$$
admin_pw="E2e-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
cleanup() { docker rm -fv "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== Starting $image"
docker run -d --name "$name" --add-host=host.docker.internal:host-gateway -p 127.0.0.1::8089 \
  -e SPLUNK_START_ARGS=--accept-license -e SPLUNK_GENERAL_TERMS=--accept-sgt-current-at-splunk-com \
  -e SPLUNK_PASSWORD="$admin_pw" -e SPLUNK_APPS_URL=/tmp/nats.tgz \
  -v "$package:/tmp/nats.tgz:ro" "$image" >/dev/null
for _ in $(seq 120); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$name")" = healthy ] && break
  sleep 5
done
[ "$(docker inspect -f '{{.State.Health.Status}}' "$name")" = healthy ]
base="https://127.0.0.1:$(docker port "$name" 8089/tcp | head -1 | sed 's/.*://')"
docker exec "$name" /opt/splunk/bin/splunk version 2>/dev/null | tail -1

# The admin password goes through a curl config on a pipe, never argv
rest() {
  local path=$1
  shift
  curl -sSk --fail-with-body --config <(printf 'user = "admin:%s"\n' "$admin_pw") "$@" "$base$path"
}
search() {
  rest /services/search/v2/jobs/export -d output_mode=csv -d earliest_time=0 \
    --data-urlencode "search=$1" | tr -d '\r'
}

echo "== Installing the decoder"
decoders=/opt/splunk/etc/apps/nats/local/decoders
docker exec -u root "$name" mkdir -p "$decoders"
docker cp "$decoder" "$name:$decoders/telemetry-decoder"
docker exec -u root "$name" sh -c "chown -R splunk:splunk /opt/splunk/etc/apps/nats/local && chmod 755 $decoders/telemetry-decoder"
docker exec -u splunk "$name" "$decoders/telemetry-decoder" -version

echo "== Creating the account"
rest /services/data/indexes -d name=telemetry >/dev/null
printf '%s' "$NATS_PASSWORD" | rest /servicesNS/nobody/nats/nats_account -d name=live \
  --data-urlencode "servers=$NATS_URL" --data-urlencode "username=$NATS_USERNAME" \
  --data-urlencode password@- >/dev/null

echo "== natssubscribe with decoder=telemetry-decoder (30s, up to 2000 messages)"
search '| natssubscribe subject="telemetry.>" account=live count=2000 duration=30 decoder=telemetry-decoder
  | eval type=mvindex(split(source, "."), -1), decoded=if(isnull(decode_error), "decoded", "failed")
  | spath | stats count by type decoded sourcetype'
search '| natssubscribe subject="telemetry.>" account=live count=2000 duration=30 decoder=telemetry-decoder
  | spath | eval type=mvindex(split(source, "."), -1)
  | stats count(VehicleSpeed) as VehicleSpeed count(Location.latitude) as Location.latitude
    count(BatteryLevel) as BatteryLevel count(createdAt) as createdAt count(vin) as vin by type'

echo "== NATS Subscribe input on telemetry.> for ${seconds}s"
rest /servicesNS/nobody/nats/nats_nats_subscribe -d name=telemetry -d account=live \
  -d 'subject=telemetry.>' -d index=telemetry -d decoder=telemetry-decoder >/dev/null
start=$(date +%s)
sleep "$seconds"
rest /servicesNS/nobody/nats/nats_nats_subscribe/telemetry -d disabled=1 >/dev/null
sleep 10 # let the last events reach the index
echo "indexed for $(($(date +%s) - start))s"

echo "-- events by record type and sourcetype (nats:json = decoded, nats:message = base64 fallback)"
search 'search index=telemetry | eval type=mvindex(split(source, "."), -1) | stats count by type sourcetype'
echo "-- vin and createdAt extracted from every event"
search 'search index=telemetry | eval type=mvindex(split(source, "."), -1)
  | stats count as events count(vin) as vin count(createdAt) as createdAt by type'
missing=$(search 'search index=telemetry | where isnull(vin) OR isnull(createdAt) | stats count' | tail -1)
echo "-- data: events carrying common signals"
search 'search index=telemetry sourcetype=nats:json source=*.data
  | stats count as events count(vin) as vin count(createdAt) as createdAt count(VehicleSpeed) as VehicleSpeed
    count(Location.latitude) as Location.latitude count(BatteryLevel) as BatteryLevel count(Gear) as Gear
    count(DoorState.DriverFront) as DoorState.DriverFront count(invalid{}) as invalid dc(vin) as vehicles'
echo "-- data: average speed and battery, Gear values"
search 'search index=telemetry sourcetype=nats:json source=*.data | stats avg(VehicleSpeed) as avg_VehicleSpeed avg(BatteryLevel) as avg_BatteryLevel values(Gear) as Gear'
echo "-- connectivity"
search 'search index=telemetry sourcetype=nats:json source=*.connectivity | stats count by status networkInterface'
echo "-- alerts (top names)"
search 'search index=telemetry sourcetype=nats:json source=*.alerts | stats count as events | appendcols [search index=telemetry sourcetype=nats:json source=*.alerts | top limit=5 alerts{}.name]'
echo "-- errors"
search 'search index=telemetry sourcetype=nats:json source=*.errors | stats count as events values(errors{}.name) as names'

failed_events=$(search 'search index=telemetry sourcetype!=nats:json | stats count' | tail -1)
failed_log=$(search 'search index=_internal sourcetype=splunkd "failed to decode" | stats count' | tail -1)
total=$(search 'search index=telemetry | stats count' | tail -1)
echo "== $total events, $failed_events not decoded, $failed_log decode failures logged, $missing without vin or createdAt"
[ "$total" -gt 0 ] && [ "$failed_events" = 0 ] && [ "$failed_log" = 0 ] && [ "$missing" = 0 ]
echo PASS
