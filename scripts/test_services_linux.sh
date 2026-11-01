#!/bin/sh
# ONLY run in a disposable root Linux CI environment. Uses real init services.
set -eu
[ "${VIORKA_DISPOSABLE_SERVICE_TEST:-}" = yes ] || { echo 'Disposable CI environment required' >&2; exit 1; }
[ "$(id -u)" = 0 ]
export HOME=/root
agent_dir=/root/.viorka-agent
[ ! -e "$agent_dir" ]
mkdir -p "$agent_dir/state"
chmod 700 /root "$agent_dir" "$agent_dir/state"
printf '{}\n' > "$agent_dir/agent.json"
printf 'preserved identity\n' > "$agent_dir/state/identity.json"
cat > "$agent_dir/viorka-agent" <<'AGENT'
#!/bin/sh
set -eu
case "$1" in
config-check) exit 0 ;;
run)
    echo $$ > /root/.viorka-agent/fixture.pid
    echo started >> /root/.viorka-agent/starts
    trap 'exit 0' TERM INT
    while :; do sleep 1 & wait $!; done ;;
*) exit 1 ;;
esac
AGENT
chmod 700 "$agent_dir/viorka-agent"
sh install.sh service
sleep 2
first=$(cat "$agent_dir/fixture.pid")
kill -0 "$first"
# An existing service must never be overwritten or enrolled again.
if sh install.sh service; then echo 'existing service unexpectedly accepted' >&2; exit 1; fi
# Repeat install through real supervisors with a local, checksummed release fixture.
fixture_dir=$(mktemp -d)
cp "$agent_dir/viorka-agent" "$fixture_dir/payload"
cat > "$fixture_dir/curl" <<'CURL'
#!/bin/sh
set -eu
while [ "$1" != --output ]; do shift; done
output=$2
shift 2
case "$1" in
    *.sha256) sha256sum "$VIORKA_TEST_PAYLOAD" | cut -d ' ' -f 1 > "$output" ;;
    *) cp "$VIORKA_TEST_PAYLOAD" "$output" ;;
esac
CURL
chmod 700 "$fixture_dir/curl"
export VIORKA_TEST_PAYLOAD="$fixture_dir/payload"
for attempt in 1 2; do
    PATH="$fixture_dir:$PATH" sh install.sh install --url=https://localhost --version=v0.1.0-test
    sleep 2
    next=$(cat "$agent_dir/fixture.pid")
    [ "$next" != "$first" ]
    kill -0 "$next"
    first=$next
done
rm -rf "$fixture_dir"
starts_before=$(wc -l < "$agent_dir/starts")
kill -KILL "$first"
# Verify actual supervisor recovery, not just the invocation of its CLI.
i=0
while [ "$(wc -l < "$agent_dir/starts")" -le "$starts_before" ]; do
    i=$((i + 1)); [ "$i" -lt 20 ]; sleep 1
done
kill -0 "$(cat "$agent_dir/fixture.pid")"
if [ -d /run/systemd/system ]; then
    systemctl is-enabled --quiet viorka-agent
    systemctl stop viorka-agent
else
    test -L /etc/runlevels/default/viorka-agent
    rc-service viorka-agent stop
fi
sleep 2
if kill -0 "$(cat "$agent_dir/fixture.pid")" 2>/dev/null; then exit 1; fi
test "$(cat "$agent_dir/state/identity.json")" = 'preserved identity'
echo 'Service start, repeated update, respawn, stop and identity preservation passed'
