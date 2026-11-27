#!/usr/bin/env bash
# Tests for check.sh with fake curl, systemctl and journalctl on PATH. Each case
# sets the fakes' scripted answers, runs check.sh under sh with second-scale
# timeouts, and asserts the HEALTH: line and the exit code.
#
#   bash .github/actions/bot-health/check_test.sh
#   CHECK=/path/to/stub.sh bash .github/actions/bot-health/check_test.sh   # the tests must then fail
set -u

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CHECK=${CHECK:-$HERE/check.sh}
SHA=0123456789abcdef0123456789abcdef01234567
OTHER=fedcba9876543210fedcba9876543210fedcba98

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAKES=$WORK/bin
mkdir -p "$FAKES"

# curl: pops the next scripted "<code> <body>" line from $TD/curl (the last one
# repeats). Writes the body to the -o file and prints the code, like -w does.
cat > "$FAKES/curl" <<'FAKE'
#!/bin/sh
out=
while [ $# -gt 0 ]; do
  case $1 in -o) out=$2; shift ;; esac
  shift
done
line=$(head -n 1 "$TD/curl")
if [ "$(wc -l < "$TD/curl")" -gt 1 ]; then sed -i 1d "$TD/curl"; fi
code=${line%% *}
body=${line#* }
[ -n "$out" ] && printf '%s' "$body" > "$out"
printf '%s' "$code"
[ "$code" = 000 ] && exit 7
exit 0
FAKE

# systemctl: "show" prints $TD/show.<n> for the n-th call (else $TD/show.default),
# "is-active" prints $TD/active, "restart" is recorded.
cat > "$FAKES/systemctl" <<'FAKE'
#!/bin/sh
case $1 in
  restart) echo restart >> "$TD/calls"; exit 0 ;;
  is-active) cat "$TD/active"; exit 0 ;;
  show)
    case "$*" in
      *--value*)
        n=$(cat "$TD/show.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$TD/show.n"
        if [ -f "$TD/show.$n" ]; then cat "$TD/show.$n"; else cat "$TD/show.default"; fi ;;
      *) echo "ActiveState=active" ;;
    esac ;;
esac
FAKE
printf '#!/bin/sh\nexit 0\n' > "$FAKES/journalctl"
chmod +x "$FAKES"/*

body() { # body <status> <version> <reasons-json>
  printf '{"status":"%s","version":"%s","uptime_seconds":5,"discord_connected":true,"openai_degraded":false,"reasons":%s}' "$1" "$2" "$3"
}

FAILED=0
PASSED=0

# run_case <name> <expected exit> <expected HEALTH regex> [VAR=value...]
# Scripted answers: $TD/curl lines, optional $TD/show.<n>, $TD/active, $TD/link.
run_case() {
  local name=$1 want_exit=$2 want_re=$3
  shift 3
  local out rc last
  out=$(env "$@" TD="$TD" PATH="$FAKES:$PATH" EXPECT="$SHA" ADDR=127.0.0.1:8086 \
    CURRENT_LINK="$TD/current" READY_TIMEOUT=3 STABLE=1 POLL=1 RETRY_SLEEP=1 NOEP_WAIT=2 \
    sh "$CHECK" 2>&1)
  rc=$?
  last=$(printf '%s\n' "$out" | tail -n 1)
  if [ "$rc" = "$want_exit" ] && [[ $last =~ $want_re ]]; then
    PASSED=$((PASSED + 1))
    echo "ok   $name -> $last"
  else
    FAILED=$((FAILED + 1))
    echo "FAIL $name: exit $rc (want $want_exit), last line '$last' (want /$want_re/)"
    printf '%s\n' "$out" | sed 's/^/     | /'
  fi
}

new_case() { # fresh state with a healthy unit and a correct release link
  TD=$(mktemp -d "$WORK/case.XXXXXX")
  export TD
  : > "$TD/calls"
  echo "200 $(body ok "$SHA" '[]')" > "$TD/curl"
  printf '1234\ninvocation-a\n' > "$TD/show.default"
  echo active > "$TD/active"
  ln -s "/opt/danbot/releases/$SHA" "$TD/current"
}

new_case
run_case "healthy" 0 '^HEALTH: ok$'

new_case
echo "503 $(body starting "$SHA" '["starting"]')" > "$TD/curl"
run_case "always 503" 1 '^HEALTH: fail not ready after 3s \(last: HTTP 503 starting/starting\)$'

new_case
echo "200 $(body ok "$OTHER" '[]')" > "$TD/curl"
run_case "200 with the wrong version" 1 '^HEALTH: fail wrong version \(running fedcba'

new_case
printf '1234\ninvocation-a\n' > "$TD/show.1"
printf '5678\ninvocation-b\n' > "$TD/show.2"
run_case "MainPID changes in the stability window" 1 '^HEALTH: fail restarted during the 1s stability window$'

new_case
echo "000 " > "$TD/curl"
run_case "connection refused, endpoint required" 1 '^HEALTH: fail not ready after 3s \(last: no answer\)$'

new_case
echo "000 " > "$TD/curl"
run_case "connection refused, endpoint optional, systemd stable" 0 '^HEALTH: ok \(no endpoint, systemd only\)$' ALLOW_NO_ENDPOINT=true

new_case
echo "000 " > "$TD/curl"
echo inactive > "$TD/active"
run_case "no endpoint but the unit is not active" 1 '^HEALTH: fail bot is inactive after' ALLOW_NO_ENDPOINT=true

new_case
echo "000 " > "$TD/curl"
rm "$TD/current"; ln -s "/opt/danbot/releases/$OTHER" "$TD/current"
run_case "no endpoint and the wrong release link" 1 '^HEALTH: fail release link is ' ALLOW_NO_ENDPOINT=true

# curl answers in order: the ready poll, then the final-check tries (the last repeats)
new_case
printf '%s\n' "200 $(body ok "$SHA" '[]')" \
  "503 $(body unhealthy "$SHA" '["discord gateway not connected"]')" "200 $(body ok "$SHA" '[]')" > "$TD/curl"
run_case "one 503 at the final check, then 200" 0 '^HEALTH: ok$'

new_case
printf '%s\n' "200 $(body ok "$SHA" '[]')" \
  "503 $(body unhealthy "$SHA" '["discord gateway not connected"]')" > "$TD/curl"
run_case "503 at every final try" 1 '^HEALTH: fail unhealthy at the final check \(last: HTTP 503 unhealthy/discord gateway not connected\)$'

new_case
rm "$TD/current"; ln -s "/opt/danbot/releases/$OTHER" "$TD/current"
run_case "wrong release link" 1 '^HEALTH: fail release link is /opt/danbot/releases/fedcba'

new_case
run_case "restart=true restarts the unit" 0 '^HEALTH: ok$' RESTART=true
if [ "$(grep -c restart "$TD/calls")" != 1 ]; then
  FAILED=$((FAILED + 1)); echo "FAIL restart=true did not call systemctl restart exactly once"
fi

new_case
run_case "restart=false does not restart" 0 '^HEALTH: ok$'
if [ -s "$TD/calls" ]; then
  FAILED=$((FAILED + 1)); echo "FAIL restart=false called systemctl restart"
fi

echo "$PASSED passed, $FAILED failed"
[ "$FAILED" = 0 ]
