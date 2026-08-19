#!/usr/bin/env bash
# run-probe.sh - deploy a Zabbix ACTIVE proxy as a single container (mTLS, 7-day offline buffer).
# No inbound port is published: an active proxy DIALS OUT to the core, so remote sites need
# only outbound access to core:10051.
#
# Usage:   ./run-probe.sh <site> <core-host> [appdata-dir]
# Example: ./run-probe.sh site1 core.example.lan /mnt/user/appdata
#
# Expects certs already present at:  <appdata>/zbx-proxy-<site>/certs/
#   ca.crt  proxy-<site>.crt  proxy-<site>.key
# And a writable data dir (SQLite spool) at:  <appdata>/zbx-proxy-<site>/data/
set -euo pipefail

SITE="${1:?usage: run-probe.sh <site> <core-host> [appdata-dir]}"
CORE="${2:?usage: run-probe.sh <site> <core-host> [appdata-dir]}"
APPDATA="${3:-/mnt/user/appdata}"
IMAGE="zabbix/zabbix-proxy-sqlite3:alpine-7.0-latest"

BASE="${APPDATA}/zbx-proxy-${SITE}"
CERTS="${BASE}/certs"
DATA="${BASE}/data"
mkdir -p "$DATA"

for f in ca.crt "proxy-${SITE}.crt" "proxy-${SITE}.key"; do
  [[ -f "${CERTS}/${f}" ]] || { echo "MISSING cert: ${CERTS}/${f}"; exit 1; }
done

docker rm -f "zbx-proxy-${SITE}" 2>/dev/null || true

docker run -d \
  --name "zbx-proxy-${SITE}" \
  --restart unless-stopped \
  -e ZBX_PROXYMODE=0 \
  -e ZBX_SERVER_HOST="${CORE}" \
  -e ZBX_HOSTNAME="proxy-${SITE}" \
  -e ZBX_PROXYOFFLINEBUFFER=168 \
  -e ZBX_PROXYLOCALBUFFER=0 \
  -e ZBX_TLSCONNECT=cert \
  -e ZBX_TLSACCEPT=cert \
  -e ZBX_TLSCAFILE=/certs/ca.crt \
  -e ZBX_TLSCERTFILE="/certs/proxy-${SITE}.crt" \
  -e ZBX_TLSKEYFILE="/certs/proxy-${SITE}.key" \
  -e ZBX_TLSSERVERCERTISSUER="CN=Monitoring Core CA" \
  -e ZBX_TLSSERVERCERTSUBJECT="CN=zabbix-core" \
  -v "${CERTS}:/certs:ro" \
  -v "${DATA}:/var/lib/zabbix" \
  -v "${DATA}/snmptraps:/var/lib/zabbix/snmptraps" \
  "${IMAGE}"

echo "Started zbx-proxy-${SITE} -> ${CORE}:10051 (active, mTLS, 7-day buffer)."
echo "Register it in the Zabbix UI as an ACTIVE proxy named 'proxy-${SITE}' with"
echo "certificate encryption before data will flow."
echo "Logs:  docker logs -f zbx-proxy-${SITE}"
