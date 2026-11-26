#!/bin/sh
# Boot recovery must work even when the installed Agent cannot execute.
# Run only on a disposable service-manager host with an empty installation.
set -eu
[ "${VIORKA_DISPOSABLE_SERVICE_TEST:-}" = yes ] && [ "$(id -u)" = 0 ]
export HOME=/root
dir=/root/.viorka-agent
[ ! -e "$dir" ] && [ ! -L "$dir" ]
mkdir -m 700 "$dir"
mkdir -m 700 "$dir/state"
test -f "$1"
cp "$1" "$dir/viorka-agent"
chmod 700 "$dir/viorka-agent"
umask 077
printf '{"panel_url":"https://127.0.0.1:1","node_name":"qa","state_dir":"%s/state","telemetry_v3":false}\n' "$dir" > "$dir/agent.json"
# This isolated identity has no corresponding Panel registration.
key=$(head -c 64 /dev/urandom | base64 | tr -d '\n')
printf '{"panel_url":"https://127.0.0.1:1","node_id":"test-node","private_key":"%s"}\n' "$key" > "$dir/state/identity.json"
unset key
sh install.sh service
service_action() {
    if [ -d /run/systemd/system ]; then systemctl "$1" viorka-agent.service
    else rc-service viorka-agent "$1"; fi
}
wait_for() {
    i=0
    until "$@"; do i=$((i+1)); [ "$i" -lt 45 ]; sleep 1; done
}
wait_for test -s "$dir/.agent-ready"
service_action stop
cp "$dir/viorka-agent" "$dir/viorka-agent.previous"
printf '#!/bin/sh\nexit 1\n' > "$dir/broken"
chmod 700 "$dir/broken"
mv "$dir/broken" "$dir/viorka-agent"
cat > "$dir/.agent-update.json" <<'JOURNAL'
{"task":"recovery-test","node":"test-node","target":{"version":"v9.9.9","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"deadline":"2026-01-01T00:00:00Z","phase":"trial"}
JOURNAL
ln -s .agent-update.json "$dir/.install-lock"
service_action start
wait_for grep -q update_rolled_back "$dir/.agent-update.json"
cmp "$dir/viorka-agent.previous" "$dir/viorka-agent"
wait_for test ! -L "$dir/.install-lock"
kill -0 "$(cat "$dir/.agent-ready")"
sh install.sh uninstall
# Preserve the fixture and leave the path free for the other service tests.
saved=$(mktemp -d)
mv "$dir" "$saved/"
