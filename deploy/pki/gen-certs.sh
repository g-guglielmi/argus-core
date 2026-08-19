#!/usr/bin/env bash
# gen-certs.sh - the monitoring PKI: ONE shared CA, ONE core (server) cert,
# and ONE UNIQUE client cert per site for mutual-TLS proxy<->server auth.
#
# SECURITY MODEL:
#   - All sites share the same CA (trust root). ca.key is the crown jewel - keep it OFFLINE.
#   - Each site gets its OWN cert/key. Never share a leaf cert across sites.
#   - Only ca.crt (public) + that site's own cert/key ever go to a probe.
#
# First run (creates CA + core cert + the 5 default sites):
#   ./gen-certs.sh
#
# ADD A NEW SITE LATER (reuses the existing CA, leaves other certs untouched):
#   ./gen-certs.sh mynewsite
#
# Regenerate an existing leaf (e.g. after a suspected key leak):
#   FORCE=1 ./gen-certs.sh proxysite      # note: FORCE regenerates only what you name
#
# Output: ./out/{ca.crt,ca.key, zabbix-core.crt/.key, proxy-<site>.crt/.key}
set -euo pipefail

OUT="${OUT:-./out}"
DAYS_CA="${DAYS_CA:-3650}"      # 10y CA
DAYS_LEAF="${DAYS_LEAF:-1825}"  # 5y leaf certs
CA_CN="Monitoring Core CA"
CORE_CN="zabbix-core"
DEFAULT_SITES=(site1 site2 site3 site4 site5)

# Sites to sign: explicit args if given, else the default fleet.
if [[ $# -gt 0 ]]; then SITES=("$@"); else SITES=("${DEFAULT_SITES[@]}"); fi

mkdir -p "$OUT"; cd "$OUT"

echo ">> CA ($CA_CN)"
if [[ ! -f ca.key ]]; then
  openssl req -x509 -newkey rsa:4096 -nodes -keyout ca.key -out ca.crt \
    -days "$DAYS_CA" -subj "/CN=${CA_CN}"
  chmod 600 ca.key
else
  echo "   ca.key exists - reusing (this is what keeps the whole fleet trusting each other)"
fi

sign_leaf() {
  local name="$1" cn="$2"
  if [[ -f "${name}.crt" && "${FORCE:-0}" != "1" ]]; then
    echo ">> ${name}: exists - skipping (FORCE=1 to regenerate this one)"
    return
  fi
  echo ">> ${name}: signing (CN=${cn})"
  openssl req -newkey rsa:2048 -nodes -keyout "${name}.key" -out "${name}.csr" \
    -subj "/CN=${cn}"
  openssl x509 -req -in "${name}.csr" -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out "${name}.crt" -days "$DAYS_LEAF" -sha256
  rm -f "${name}.csr"; chmod 600 "${name}.key"
}

# Core cert only on first run (leave alone when just adding a site).
sign_leaf "zabbix-core" "$CORE_CN"
for s in "${SITES[@]}"; do sign_leaf "proxy-${s}" "proxy-${s}"; done

echo
echo "Done. Files in: $OUT"
echo "  CORE  needs: ca.crt, zabbix-core.crt, zabbix-core.key"
echo "  PROBE needs: ca.crt, proxy-<site>.crt, proxy-<site>.key   (that site ONLY)"
echo "  OFFLINE:     ca.key                                       (never on a probe)"
echo
echo "Pin on the server side (per proxy in the Zabbix UI):"
echo "  Issuer  = CN=${CA_CN}"
echo "  Subject = CN=proxy-<site>"
