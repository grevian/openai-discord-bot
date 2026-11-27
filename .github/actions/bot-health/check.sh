#!/bin/sh
# Health check that runs ON the bot instance, as root, over SSM Run Command
# (see action.yml and run.sh). POSIX sh: AWS-RunShellScript runs these lines as
# one script, and the settings arrive as a leading block of VAR=value lines.
#
# Settings (environment):
#   EXPECT             required, the git sha the instance must run
#   RESTART            true: systemctl restart bot first (default false)
#   READY_TIMEOUT      seconds to wait for /healthz to answer 200 (default 120)
#   STABLE             seconds the unit must keep the same process (default 30)
#   ALLOW_NO_ENDPOINT  true: a release from before /healthz existed may pass on
#                      systemd state and the release link alone (default false)
#   ADDR               host:port of /healthz (default 127.0.0.1:8086)
#   CURRENT_LINK       the release symlink (default /opt/danbot/current)
#   POLL, RETRY_SLEEP, NOEP_WAIT   timings in seconds, overridable for tests
#
# The last line printed is "HEALTH: ok[ <note>]" or "HEALTH: fail <reason>",
# and the exit status is 0 for ok, 1 for fail. The bot's own log lines can hold
# user text, so only the "danbot-prepare:" lines and the health body (booleans,
# version, reasons) are ever printed.

: "${EXPECT:?EXPECT is required}"
RESTART=${RESTART:-false}
READY_TIMEOUT=${READY_TIMEOUT:-120}
STABLE=${STABLE:-30}
ALLOW_NO_ENDPOINT=${ALLOW_NO_ENDPOINT:-false}
ADDR=${ADDR:-127.0.0.1:8086}
CURRENT_LINK=${CURRENT_LINK:-/opt/danbot/current}
POLL=${POLL:-3}
RETRY_SLEEP=${RETRY_SLEEP:-5}
NOEP_WAIT=${NOEP_WAIT:-20}

BODY=$(mktemp) || exit 2
trap 'rm -f "$BODY"' EXIT

now() { date +%s; }

# finish ok|fail <text>: print the evidence, then the verdict as the last line.
finish() {
  echo "-- unit state"
  systemctl show bot -p ActiveState -p SubState -p Result -p NRestarts -p ExecMainStatus 2>&1 | head -n 8
  echo "-- release link: $(readlink "$CURRENT_LINK" 2>&1)"
  echo "-- danbot-prepare (last 3)"
  if command -v journalctl >/dev/null 2>&1; then
    journalctl -u bot -n 400 --no-pager -o cat 2>/dev/null | grep -a '^danbot-prepare:' | tail -n 3 | cut -c1-200
  fi
  if [ "$1" = ok ]; then
    echo "HEALTH: ok${2:+ $2}"
    exit 0
  fi
  echo "HEALTH: fail $2"
  exit 1
}

# fetch: query /healthz once. Sets CODE (000 = no answer), STATUS, REASONS,
# VERSION and MATCH (1 when the body carries "version":"<EXPECT>").
fetch() {
  : > "$BODY"
  CODE=$(curl -sS --max-time 3 -o "$BODY" -w '%{http_code}' "http://$ADDR/healthz" 2>/dev/null) || CODE=000
  case $CODE in [0-9][0-9][0-9]) ;; *) CODE=000 ;; esac
  JSON=$(head -c 2048 "$BODY" | tr -d '\n')
  STATUS=$(printf '%s' "$JSON" | sed -n 's/.*"status":"\([a-z]*\)".*/\1/p' | tr -cd '[:lower:]')
  VERSION=$(printf '%s' "$JSON" | sed -n 's/.*"version":"\([0-9a-zA-Z._-]*\)".*/\1/p' | tr -cd '0-9a-zA-Z._-')
  REASONS=$(printf '%s' "$JSON" | sed -n 's/.*"reasons":\[\([^]]*\)\].*/\1/p' | tr -cd 'a-zA-Z0-9 ,._-')
  MATCH=0
  case $JSON in *'"version":"'"$EXPECT"'"'*) MATCH=1 ;; esac
}

# unit_id: the unit's process and invocation; any change means a restart.
unit_id() { systemctl show -p MainPID -p InvocationID --value bot 2>/dev/null | tr '\n' ' '; }

if [ "$RESTART" = true ]; then
  echo "-- restarting bot"
  # No exit on failure: a failed danbot-prepare leaves the unit restarting in a
  # loop, and the wait below then fails with a reason instead.
  systemctl restart bot || echo "systemctl restart bot failed ($?)"
fi

START=$(now)
DEADLINE=$((START + READY_TIMEOUT))
ANSWERED=0 READY=0 NOEP=0
LAST="no answer"
WRONG_VERSION=
while :; do
  fetch
  WRONG_VERSION=
  if [ "$CODE" = 000 ]; then
    LAST="no answer"
  else
    ANSWERED=1
    LAST="HTTP $CODE ${STATUS:-?}/${REASONS:--}"
    if [ "$CODE" = 200 ] && [ "$MATCH" = 0 ]; then WRONG_VERSION=${VERSION:-unknown}; fi
  fi
  if [ "$CODE" = 200 ] && [ "$MATCH" = 1 ]; then READY=1; break; fi
  NOW=$(now)
  if [ "$ALLOW_NO_ENDPOINT" = true ] && [ "$ANSWERED" = 0 ] && [ $((NOW - START)) -ge "$NOEP_WAIT" ]; then
    NOEP=1
    break
  fi
  [ "$NOW" -ge "$DEADLINE" ] && break
  sleep "$POLL"
done

if [ "$READY" = 1 ]; then
  echo "-- /healthz answered: $LAST after $(($(now) - START))s"
elif [ "$NOEP" = 1 ]; then
  echo "-- /healthz never answered in ${NOEP_WAIT}s; checking systemd state only"
elif [ -n "$WRONG_VERSION" ]; then
  finish fail "wrong version (running $WRONG_VERSION, expected $EXPECT)"
else
  finish fail "not ready after ${READY_TIMEOUT}s (last: $LAST)"
fi

BEFORE=$(unit_id)
sleep "$STABLE"
AFTER=$(unit_id)
if [ "$BEFORE" != "$AFTER" ]; then
  finish fail "restarted during the ${STABLE}s stability window"
fi
ACTIVE=$(systemctl is-active bot 2>&1)
if [ "$ACTIVE" != active ]; then
  finish fail "bot is $ACTIVE after the ${STABLE}s stability window"
fi

if [ "$NOEP" = 0 ]; then
  # A single gateway reconnect at this moment must not fail a good deploy.
  TRY=1 PASSED=0
  while [ "$TRY" -le 3 ]; do
    fetch
    if [ "$CODE" = 200 ] && [ "$MATCH" = 1 ]; then PASSED=1; break; fi
    LAST="HTTP $CODE ${STATUS:-?}/${REASONS:--}"
    [ "$CODE" = 000 ] && LAST="no answer"
    [ "$TRY" -lt 3 ] && sleep "$RETRY_SLEEP"
    TRY=$((TRY + 1))
  done
  if [ "$PASSED" = 0 ]; then
    finish fail "unhealthy at the final check (last: $LAST)"
  fi
fi

LINK=$(readlink "$CURRENT_LINK" 2>&1)
case $LINK in
  */"$EXPECT" | "$EXPECT") ;;
  *) finish fail "release link is $LINK, expected $EXPECT" ;;
esac

if [ "$NOEP" = 1 ]; then
  finish ok "(no endpoint, systemd only)"
fi
finish ok
