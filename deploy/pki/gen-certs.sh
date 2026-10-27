#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

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
# RENEW THE CA before its end (a core VM with Argus does this by itself, two years ahead):
#   RENEW_CA=1 ./gen-certs.sh
# signs ca.crt again with ca.key: the same name and key, ten more years, so every certificate it
# signed stays trusted. Then put the new ca.crt where the old one is: Argus's /ca mount (Argus hands
# it to the probes), Zabbix's TLSCAFile (restart zabbix-server) and the PCs that trust it.
#
# Output: ./out/{ca.crt,ca.key, zabbix-core.crt/.key, proxy-<site>.crt/.key}
set -euo pipefail

OUT="${OUT:-./out}"
DAYS_CA="${DAYS_CA:-3650}"      # 10y CA
DAYS_LEAF="${DAYS_LEAF:-730}"   # 2y leaf certs: Zabbix pins issuer+subject and there is no revocation, so a leaked key lives until its certificate ends or the proxy is re-keyed
CA_CN="Monitoring Core CA"
CORE_CN="zabbix-core"
DEFAULT_SITES=(site1 site2 site3 site4 site5)

# Sites to sign: explicit args if given, else the default fleet.
if [[ $# -gt 0 ]]; then SITES=("$@"); else SITES=("${DEFAULT_SITES[@]}"); fi

mkdir -p "$OUT"; cd "$OUT"

if [[ "${RENEW_CA:-0}" == "1" ]]; then
  [[ -f ca.crt && -f ca.key ]] || { echo "no ca.crt + ca.key in $OUT to renew" >&2; exit 1; }
  echo ">> CA ($CA_CN): signing it again with its own key"
  openssl x509 -in ca.crt -signkey ca.key -days "$DAYS_CA" -set_serial "0x$(openssl rand -hex 16)" -out ca.crt.new
  if [[ "$(openssl x509 -in ca.crt.new -noout -pubkey)" != "$(openssl x509 -in ca.crt -noout -pubkey)" ]]; then
    rm -f ca.crt.new; echo "ca.key is not the key of ca.crt: nothing renewed" >&2; exit 1
  fi
  cp -p ca.crt ca.crt.old && mv -f ca.crt.new ca.crt
  echo "   $(openssl x509 -in ca.crt -noout -enddate) (the old one is in ca.crt.old)"
  echo "   Put this ca.crt where the old one is: Argus's /ca mount, Zabbix's TLSCAFile (restart zabbix-server), the PCs."
  exit 0
fi

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
