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
# Then the next line: the database gets what zabbix-server writes into it at every start (which
# TimescaleDB lines it supports), and the request "Update now" leaves for a new line runs:
#   - without a backup from the last day nothing is touched,
#   - with one, the packages and the database are on the newest release of the highest line Zabbix
#     supports, held, the pin rewritten for that line, every row there, compressed chunks readable,
#     the compression job running,
#   - nothing is offered beyond what Zabbix supports.
# And the way back, which is a restore: argus-restore's own functions put the line the dump was
# made on back (packages, hold, pin) and restore a dump taken before the move.
# systemd is not there in a container: `systemctl` is a stand-in that restarts the real cluster and
# notes what was asked of zabbix-server.
#
# Usage (the pull request check runs exactly this):
#   docker run --rm -v "$PWD:/src:ro" debian:13 bash /src/deploy/core/host/tests/tsdb_live_test.sh
set -u
SRC="${ARGUS_SRC:-/src}"
FROM_VERSION="${TSDB_FROM:-2.28.3}"
CEIL="${TSDB_CEIL:-2.30}"   # the highest line the stand-in for Zabbix says it supports
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
apt-get install -y -qq --no-install-recommends ca-certificates wget gnupg util-linux python3 "postgresql-$PG" >/dev/null 2>&1 || die "could not install PostgreSQL"
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

runuser -u postgres -- createuser zabbix || die "createuser"
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

# ---------------------------------------------------------------------------------------------
# The next line.
lfield() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" /state/tsdb-line-result.json 2>/dev/null; }
PINFILE=/etc/apt/preferences.d/timescaledb-pin.pref
echo "the next line: Zabbix says how far it supports TimescaleDB"
check "nothing is offered before Zabbix has said" "$(run status)" "$NEW $NEW $NEW"
STATUS_JSON='[{"database":"PostgreSQL","current_version":"17.12","min_version":"10.9","max_version":"18.x","flag":0},{"database":"TimescaleDB","current_version":"'"$NEW"'","min_version":"2.0.1","max_version":"'"$CEIL"'","min_supported_version":"2.13.0","flag":0}]'
sql "CREATE TABLE config (configid bigint PRIMARY KEY, dbversion_status text NOT NULL DEFAULT '')" >/dev/null || die "the config table"
sql "INSERT INTO config (configid, dbversion_status) VALUES (1, \$j\$$STATUS_JSON\$j\$)" >/dev/null || die "dbversion_status"
STATUS="$(run status)"
LINE="$(printf '%s' "$STATUS" | awk '{print $4}')"
echo "    | $STATUS"
case "$LINE" in "$CEIL".*) echo "  ok   the line candidate is on the $CEIL line ($LINE)" ;; *) echo "  FAIL the line candidate is <$LINE>, not on the $CEIL line"; fail=1 ;; esac
[ -n "$LINE" ] || die "no line candidate: nothing to test"

echo "a dump from before the move, for the way back"
# Zabbix's own tables, the ones a restore looks for before it starts anything on the database.
sql "CREATE TABLE dbversion (dbversionid bigint PRIMARY KEY, mandatory integer NOT NULL DEFAULT 0)" >/dev/null || die "the dbversion table"
sql "CREATE TABLE hosts (hostid bigint PRIMARY KEY, host text NOT NULL DEFAULT '')" >/dev/null || die "the hosts table"
sql "CREATE TABLE items (itemid bigint PRIMARY KEY, hostid bigint NOT NULL DEFAULT 0)" >/dev/null || die "the items table"
# The folder and its files are root's alone, as /var/backups/argus is on a core. A folder the
# postgres user could read hid "could not open input file: Permission denied" from this test until
# the first real restore: the restore has to work with what a real machine has.
install -d -m 0700 -o root /tmp/wayback
runuser -u postgres -- pg_dump -Fc -Z 6 -d zabbix > /tmp/wayback/zabbix.dump || die "pg_dump"
printf -- '-- roles\n' > /tmp/wayback/postgres-globals.sql
chmod 0600 /tmp/wayback/zabbix.dump /tmp/wayback/postgres-globals.sql
if runuser -u postgres -- test -r /tmp/wayback/zabbix.dump; then die "the postgres user can read the dump: this test would prove nothing"; fi

echo "without a backup from the last day the line stays"
: > $CALLS
rm -f /state/backup-status.json
echo '{"at":3,"by":"test"}' > /state/tsdb-line-request.json
run 2>&1 | sed 's/^/    | /'
echo "    | $(cat /state/tsdb-line-result.json 2>/dev/null)"
check "it says failed"               "$(lfield state)" failed
has   "and why"                      "$(lfield detail)" "no backup of this core succeeded in the last 24 hours"
check "nothing was stopped"          "$(grep -c 'zabbix-server\|postgresql' $CALLS)" 0
check "the database is still on $NEW" "$(sql "select extversion from pg_extension where extname = 'timescaledb'")" "$NEW"
check "the pin is still on 2.28"     "$(grep -c 'Pin: version 2.28' $PINFILE)" 1

echo "Update now on the new line: the whole way, on a real PostgreSQL"
: > $CALLS
printf '{"last_ok_at": %d}\n' "$(date +%s)" > /state/backup-status.json
echo '{"at":4,"by":"test"}' > /state/tsdb-line-request.json
run 2>&1 | sed 's/^/    | /'
echo "    | $(cat /state/tsdb-line-result.json 2>/dev/null)"
check "it says done"                 "$(lfield state)" done
check "from and to"                  "$(lfield from) $(lfield to)" "$NEW $LINE"
check "the request is consumed"      "$(ls /state/tsdb-line-request.json 2>/dev/null)" ""
check "the database is on $LINE"     "$(sql "select extversion from pg_extension where extname = 'timescaledb'")" "$LINE"
check "the package is on $LINE"      "$(dpkg-query -W -f='${Version}' "$META" | sed 's/[~-].*//')" "$LINE"
check "the loader too"               "$(dpkg-query -W -f='${Version}' "$LOADER" | sed 's/[~-].*//')" "$LINE"
check "both are held again"          "$(apt-mark showhold | grep -c '^timescaledb-2-')" 2
check "the pin is on the $CEIL line" "$(grep -c "Pin: version $CEIL\\.\\*" $PINFILE)" 1
check "apt offers nothing beyond it" "$(apt-cache policy "$META" | awk '/Candidate:/{print $2}' | sed 's/[~-].*//')" "$LINE"
check "every row is still there, compressed chunks read" "$(sql "select count(*) || ' ' || sum(value) from history")" "$ROWS"
has   "Zabbix was stopped first"     "$(tr '\n' ';' < $CALLS)" "stop zabbix-server;restart postgresql;start zabbix-server"
sql "INSERT INTO history (itemid, clock, value) SELECT i, extract(epoch FROM now())::integer - 14 * 86400 - s * 600, 1 FROM generate_series(1, 20) i, generate_series(0, 20) s" >/dev/null || { echo "  FAIL rows could not be written on $LINE"; fail=1; }
if sql "CALL run_job($JOB)" >/tmp/job.out 2>&1; then echo "  ok   the compression job runs on $LINE"; else echo "  FAIL the job failed on $LINE: $(cat /tmp/job.out)"; fail=1; fi
check "the reporter reads all three on $LINE, and no further line" "$(run status)" "$LINE $LINE $LINE"
if flock -n /run/argus-db-update.lock true; then echo "  ok   nothing kept the lock"; else echo "  FAIL something started by the move still holds the lock"; fail=1; fi

echo "nothing beyond what Zabbix supports"
: > $CALLS
echo '{"at":5,"by":"test"}' > /state/tsdb-line-request.json
timeout 120 bash -c 'ARGUS_STATE_DIR=/state ARGUS_OS_REPORT=/bin/true bash "$0"' "$SCRIPT" >/dev/null 2>&1 || { echo "  FAIL the run did not end"; fail=1; }
check "it says failed"               "$(lfield state)" failed
has   "and why"                      "$(lfield detail)" "nothing to move to: Zabbix on this core supports TimescaleDB up to $CEIL"
check "nothing was stopped"          "$(grep -c 'zabbix-server\|postgresql' $CALLS)" 0

# ---------------------------------------------------------------------------------------------
# The way back: a restore. argus-restore's own functions, on the dump from before the move.
echo "the way back: the restore puts the line the dump was made on in place, then the dump"
: > $CALLS
# A machine fresh from its image has no package lists at all (the image build removes them), and a
# restore is what such a machine is for: the tool has to read the repositories by itself, nobody
# runs apt-get update for it.
rm -rf /var/lib/apt/lists/*
check "no package list knows a TimescaleDB before the restore" "$(apt-cache madison "$META" 2>/dev/null | wc -l | tr -d ' ')" 0
ARGUS_BACKUP_DIR=/tmp/wayback python3 -B - "$SRC/deploy/core/host/argus-restore" "$NEW" /tmp/wayback <<'PY' 2>&1 | sed 's/^/    | /'
import importlib.machinery, importlib.util, sys
loader = importlib.machinery.SourceFileLoader("argus_restore", sys.argv[1])
r = importlib.util.module_from_spec(importlib.util.spec_from_loader("argus_restore", loader))
loader.exec_module(r)
fit = r.timescaledb_fit({"versions": {"timescaledb_extension": sys.argv[2]}})
print("the restore would install:", fit)
if not fit:
    sys.exit("the repository does not offer the version the dump needs")
try:
    r.install_timescaledb(fit)
    r.restore_zabbix(sys.argv[3])
except r.Fail as e:
    sys.exit("argus-restore: %s" % e)
PY
check "the database is back on $NEW" "$(sql "select extversion from pg_extension where extname = 'timescaledb'")" "$NEW"
check "the package is back on $NEW"  "$(dpkg-query -W -f='${Version}' "$META" | sed 's/[~-].*//')" "$NEW"
check "the loader too"               "$(dpkg-query -W -f='${Version}' "$LOADER" | sed 's/[~-].*//')" "$NEW"
check "both are held"                "$(apt-mark showhold | grep -c '^timescaledb-2-')" 2
check "the pin is back on 2.28"      "$(grep -c 'Pin: version 2.28' $PINFILE)" 1
check "the rows are those of the dump" "$(sql "select count(*) || ' ' || sum(value) from history")" "$ROWS"
check "the restore read the package lists itself" "$([ "$(apt-cache madison "$META" 2>/dev/null | wc -l)" -gt 0 ] && echo yes || echo no)" yes
has   "PostgreSQL was restarted on the old packages" "$(tr '\n' ';' < $CALLS)" "restart postgresql"
if sql "CALL run_job($JOB)" >/tmp/job.out 2>&1; then echo "  ok   the compression job runs after the restore"; else echo "  FAIL the job failed after the restore: $(cat /tmp/job.out)"; fail=1; fi
check "and the line is on offer again" "$(run status)" "$NEW $NEW $NEW $LINE"

if [ "$fail" = 0 ]; then echo "PASS"; else echo "FAILED"; fi
exit $fail
