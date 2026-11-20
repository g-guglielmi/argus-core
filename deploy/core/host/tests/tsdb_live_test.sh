#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi
#
# argus-tsdb-update against the real thing. The unit tests (test_host_scripts.py) fake apt and psql,
# so they prove the order of the steps, not that PostgreSQL and TimescaleDB take them. This builds
# what a core has, inside a throwaway Debian 13 container: PostgreSQL 17 from Debian (with its
# security updates, as a core's nightly patching leaves it), TimescaleDB 2.28.3 from the vendor's
# repository, held and pinned to 2.28 the way setup-core.sh does it, and a database with a
# Zabbix-shaped hypertable, rows in it and a compression policy. Then the script runs, from a
# request file as "Update now" leaves one, and this looks at what is true afterwards:
#   - the packages and the database are on the newest 2.28.x, the hold and the pin still there,
#   - every row is still there,
#   - the compression job runs and compresses,
#   - a second request finds nothing to do.
# systemd is not there in a container: `systemctl` is a stand-in that restarts the real cluster and
# notes what was asked of zabbix-server.
#
# Usage (the pull request check runs exactly this):
#   docker run --rm -v "$PWD:/src:ro" debian:13 bash /src/deploy/core/host/tests/tsdb_live_test.sh
set -u
SRC="${ARGUS_SRC:-/src}"
FROM_VERSION="${TSDB_FROM:-2.28.3}"
PG=17
if [ "$(id -u)" != 0 ] || [ ! -f /etc/debian_version ] || [ ! -f "$SRC/deploy/core/host/argus-tsdb-update" ]; then
  echo "SKIP: this runs as root in a Debian container with the repository at $SRC (see the header)"; exit 0
fi

fail=0
check() { # desc got want
  if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1: got <$2> want <$3>"; fail=1; fi
}
has() { # desc text part
  case "$2" in *"$3"*) echo "  ok   $1" ;; *) echo "  FAIL $1: <$2> lacks <$3>"; fail=1 ;; esac
}
die() { echo "FAILED: $*"; exit 1; }
sql() { runuser -u postgres -- psql -X -At -v ON_ERROR_STOP=1 -d zabbix -c "$1"; }

echo "setting up: PostgreSQL $PG and TimescaleDB $FROM_VERSION, as a core has them"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null || die "apt-get update"
apt-get install -y -qq --no-install-recommends ca-certificates wget gnupg util-linux "postgresql-$PG" >/dev/null 2>&1 || die "could not install PostgreSQL"
. /etc/os-release
echo "deb https://packagecloud.io/timescale/timescaledb/${ID}/ ${VERSION_CODENAME} main" > /etc/apt/sources.list.d/timescaledb.list
wget --quiet -O /tmp/timescaledb.gpgkey https://packagecloud.io/timescale/timescaledb/gpgkey || die "could not fetch the TimescaleDB repository key"
gpg --dearmor -o /etc/apt/trusted.gpg.d/timescaledb.gpg < /tmp/timescaledb.gpgkey
apt-get update -qq >/dev/null || die "apt-get update with the TimescaleDB repository"
META="timescaledb-2-postgresql-$PG"; LOADER="timescaledb-2-loader-postgresql-$PG"
OLD="$(apt-cache madison "$META" | awk '{print $3}' | grep -E "^${FROM_VERSION//./\\.}[~-]" | head -n1)"
[ -n "$OLD" ] || die "the repository has no $META $FROM_VERSION"
apt-get install -y -qq --no-install-recommends "$META=$OLD" "$LOADER=$OLD" >/dev/null 2>&1 || die "could not install TimescaleDB $OLD"
apt-mark hold "$META" "$LOADER" >/dev/null
cat > /etc/apt/preferences.d/timescaledb-pin.pref <<'PIN'
Package: timescaledb-2-*
Pin: version 2.28.*
Pin-Priority: 1001
PIN
echo "shared_preload_libraries = 'timescaledb'" > "/etc/postgresql/$PG/main/conf.d/timescaledb.conf"
pg_ctlcluster "$PG" main start || die "PostgreSQL did not start"
for _ in $(seq 1 30); do runuser -u postgres -- psql -X -At -c "select 1" >/dev/null 2>&1 && break; sleep 1; done
echo "    PostgreSQL $(runuser -u postgres -- psql -X -At -c 'show server_version'), TimescaleDB package $OLD"

runuser -u postgres -- createdb zabbix || die "createdb"
sql "CREATE EXTENSION timescaledb" >/dev/null || die "CREATE EXTENSION"
sql "CREATE TABLE history (itemid bigint NOT NULL, clock integer NOT NULL, value double precision NOT NULL, ns integer NOT NULL DEFAULT 0)" >/dev/null
sql "SELECT create_hypertable('history', 'clock', chunk_time_interval => 86400)" >/dev/null || die "create_hypertable"
sql "CREATE FUNCTION zbx_ts_unix_now() RETURNS integer LANGUAGE SQL STABLE AS 'SELECT extract(epoch FROM now())::integer'" >/dev/null
sql "SELECT set_integer_now_func('history', 'zbx_ts_unix_now')" >/dev/null || die "set_integer_now_func"
sql "ALTER TABLE history SET (timescaledb.compress, timescaledb.compress_segmentby = 'itemid', timescaledb.compress_orderby = 'clock,ns')" >/dev/null || die "enable compression"
sql "SELECT add_compression_policy('history', compress_after => 612000)" >/dev/null || die "add_compression_policy"
sql "INSERT INTO history (itemid, clock, value) SELECT i, extract(epoch FROM now())::integer - d * 86400 - s * 600, i * 1.5 + s FROM generate_series(1, 20) i, generate_series(0, 12) d, generate_series(0, 20) s" >/dev/null || die "insert"
ROWS="$(sql "select count(*) || ' ' || sum(value) from history")"
check "the database starts on $FROM_VERSION" "$(sql "select extversion from pg_extension where extname = 'timescaledb'")" "$FROM_VERSION"

# systemd's part: the real cluster is restarted, zabbix-server is only noted.
CALLS=/tmp/systemctl.calls
cat > /usr/local/bin/systemctl <<EOF
#!/usr/bin/env bash
echo "\$*" >> $CALLS
case "\$*" in
  "restart postgresql") pg_ctlcluster $PG main restart </dev/null >/dev/null 2>&1 ;;
  "is-active --quiet zabbix-server") exit 0 ;;
esac
exit 0
EOF
printf '#!/usr/bin/env bash\necho "logger: $*"\n' > /usr/local/bin/logger
chmod 0755 /usr/local/bin/systemctl /usr/local/bin/logger
install -d /state
SCRIPT="$SRC/deploy/core/host/argus-tsdb-update"
run() { ARGUS_STATE_DIR=/state ARGUS_OS_REPORT=/bin/true bash "$SCRIPT" "$@"; }
field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" /state/tsdb-update-result.json 2>/dev/null; }

echo "what the reporter reads before the update"
STATUS="$(run status)"
NEW="$(printf '%s' "$STATUS" | awk '{print $2}')"
echo "    | $STATUS"
check "installed, and the database on it" "$(printf '%s' "$STATUS" | awk '{print $1, $3}')" "$FROM_VERSION $FROM_VERSION"
case "$NEW" in 2.28.*) echo "  ok   the candidate is on the 2.28 line ($NEW)" ;; *) echo "  FAIL the candidate is <$NEW>, not on the 2.28 line"; fail=1 ;; esac
if [ "$NEW" = "$FROM_VERSION" ]; then die "the repository offers nothing newer than $FROM_VERSION on the 2.28 line: nothing to test"; fi

echo "Update now: the whole way, on a real PostgreSQL"
echo '{"at":1,"by":"test"}' > /state/tsdb-update-request.json
run 2>&1 | sed 's/^/    | /'
echo "    | $(cat /state/tsdb-update-result.json 2>/dev/null)"
check "it says done"                 "$(field state)" done
check "from and to"                  "$(field from) $(field to)" "$FROM_VERSION $NEW"
check "the request is consumed"      "$(ls /state/tsdb-update-request.json 2>/dev/null)" ""
check "the database is on $NEW"      "$(sql "select extversion from pg_extension where extname = 'timescaledb'")" "$NEW"
check "the package is on $NEW"       "$(dpkg-query -W -f='${Version}' "$META" | sed 's/[~-].*//')" "$NEW"
check "the loader too"               "$(dpkg-query -W -f='${Version}' "$LOADER" | sed 's/[~-].*//')" "$NEW"
check "both are held again"          "$(apt-mark showhold | grep -c '^timescaledb-2-')" 2
check "the pin is still there"       "$(grep -c 'Pin: version 2.28' /etc/apt/preferences.d/timescaledb-pin.pref)" 1
check "every row is still there"     "$(sql "select count(*) || ' ' || sum(value) from history")" "$ROWS"
has   "Zabbix was stopped first"     "$(tr '\n' ';' < $CALLS)" "stop zabbix-server;restart postgresql;start zabbix-server"

echo "the compression job runs on the new version"
JOB="$(sql "select job_id from timescaledb_information.jobs where hypertable_name = 'history' and proc_name like '%compress%' or proc_name like '%columnstore%' limit 1")"
if sql "CALL run_job($JOB)" >/tmp/job.out 2>&1; then echo "  ok   the job ran"; else echo "  FAIL the job failed: $(cat /tmp/job.out)"; fail=1; fi
COMPRESSED="$(sql "select count(*) from timescaledb_information.chunks where hypertable_name = 'history' and is_compressed")"
if [ "${COMPRESSED:-0}" -gt 0 ] 2>/dev/null; then echo "  ok   $COMPRESSED chunks are compressed"; else echo "  FAIL no chunk was compressed"; fail=1; fi
check "and every row is still there" "$(sql "select count(*) || ' ' || sum(value) from history")" "$ROWS"

echo "the lock is free again, though the PostgreSQL the update restarted is still running"
if flock -n /run/argus-db-update.lock true; then echo "  ok   nothing kept the lock"; else echo "  FAIL something started by the update still holds the lock"; fail=1; fi

echo "a second request finds nothing to do"
: > $CALLS
echo '{"at":2,"by":"test"}' > /state/tsdb-update-request.json
timeout 120 bash -c 'ARGUS_STATE_DIR=/state ARGUS_OS_REPORT=/bin/true bash "$0"' "$SCRIPT" >/dev/null 2>&1 || { echo "  FAIL the second run did not end"; fail=1; }
check "it says so"                   "$(field state) $(sed -n 's/.*"detail":"\([^"]*\)".*/\1/p' /state/tsdb-update-result.json)" "done already up to date"
check "nothing was stopped"          "$(grep -c 'zabbix-server\|postgresql' $CALLS)" 0
check "the reporter reads all three on $NEW" "$(run status)" "$NEW $NEW $NEW"

if [ "$fail" = 0 ]; then echo "PASS"; else echo "FAILED"; fi
exit $fail
