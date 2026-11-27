#!/usr/bin/env bash
# Tests for run.sh with a fake `aws`: input validation, the command that would
# be sent to the instance, and how the instance's last line becomes result/reason.
#   bash .github/actions/bot-health/run_test.sh
set -u

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SHA=0123456789abcdef0123456789abcdef01234567
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

# A stand-in for check.sh that proves the settings reach the instance script.
cat > "$WORK/check-stub.sh" <<'STUB'
echo "stub sees EXPECT=$EXPECT RESTART=$RESTART READY_TIMEOUT=$READY_TIMEOUT STABLE=$STABLE NOEP=$ALLOW_NO_ENDPOINT ADDR=$ADDR"
echo "HEALTH: ok"
STUB

# aws: records every call, answers describe-instances, remembers the
# parameters of send-command and "runs" them on get-command-invocation.
cat > "$WORK/bin/aws" <<'FAKE'
#!/bin/bash
echo "$*" >> "$TD/aws.calls"
case "$1 $2" in
  "ec2 describe-instances") printf '%s\n' "${FAKE_IDS-i-0abc123}" ;;
  "ssm send-command")
    while [ $# -gt 0 ]; do [ "$1" = --parameters ] && printf '%s' "$2" > "$TD/params.json"; shift; done
    echo cmd-1 ;;
  "ssm get-command-invocation")
    case "$*" in
      *"--query Status"*) echo "${FAKE_STATUS:-Success}" ;;
      *) if [ -n "${FAKE_OUT+x}" ]; then out=$FAKE_OUT; else out=$(jq -r '.commands[]' "$TD/params.json" | sh 2>&1); fi
         jq -nc --arg o "$out" '{out: $o, err: ""}' ;;
    esac ;;
esac
FAKE
chmod +x "$WORK/bin/aws"

FAILED=0
PASSED=0

# run_case <name> <expected exit> <expected result> <reason regex> [VAR=value...]
run_case() {
  local name=$1 want_exit=$2 want_result=$3 want_reason=$4
  shift 4
  TD=$(mktemp -d "$WORK/case.XXXXXX")
  : > "$TD/out" && : > "$TD/summary" && : > "$TD/aws.calls"
  local log rc result reason
  log=$(env TD="$TD" PATH="$WORK/bin:$PATH" GITHUB_OUTPUT="$TD/out" GITHUB_STEP_SUMMARY="$TD/summary" \
    CHECK_SCRIPT="$WORK/check-stub.sh" IN_EXPECT="$SHA" IN_RESTART=true IN_READY_TIMEOUT=120 \
    IN_STABLE=30 IN_ALLOW_NO_ENDPOINT=false IN_ADDR=127.0.0.1:8086 IN_INSTANCE_WAIT=0 \
    IN_FAIL_ON_UNHEALTHY=true "$@" bash "$HERE/run.sh" 2>&1)
  rc=$?
  printf '%s\n' "$log" > "$TD/log"
  result=$(sed -n 's/^result=//p' "$TD/out")
  reason=$(sed -n 's/^reason=//p' "$TD/out")
  if [ "$rc" = "$want_exit" ] && [ "$result" = "$want_result" ] && [[ $reason =~ $want_reason ]]; then
    PASSED=$((PASSED + 1))
    echo "ok   $name -> $result: $reason"
  else
    FAILED=$((FAILED + 1))
    echo "FAIL $name: exit $rc (want $want_exit), result '$result' (want $want_result), reason '$reason' (want /$want_reason/)"
    printf '%s\n' "$log" | sed 's/^/     | /'
  fi
}

no_aws_calls() { # no_aws_calls <name>: invalid input must never reach AWS
  if [ -s "$TD/aws.calls" ]; then FAILED=$((FAILED + 1)); echo "FAIL $1: aws was called"; fi
}

run_case "healthy, settings reach the instance script" 0 ok '^$'
grep -q "stub sees EXPECT=$SHA RESTART=true READY_TIMEOUT=120 STABLE=30 NOEP=false ADDR=127.0.0.1:8086" "$TD/summary" \
  || { FAILED=$((FAILED + 1)); echo "FAIL the summary lacks the instance output"; cat "$TD/summary"; }
jq -e '.executionTimeout == ["750"] and (.commands | length) > 8' "$TD/params.json" > /dev/null \
  || { FAILED=$((FAILED + 1)); echo "FAIL unexpected SSM parameters"; cat "$TD/params.json"; }

run_case "ok with a note" 0 ok '^\(no endpoint' FAKE_OUT=$'noise\nHEALTH: ok (no endpoint, systemd only)'
run_case "instance says fail" 1 fail '^not ready after 120s' FAKE_OUT=$'x\nHEALTH: fail not ready after 120s (last: no answer)' FAKE_STATUS=Failed
run_case "fail with fail-on-unhealthy=false still reports fail" 0 fail '^wrong version' \
  FAKE_OUT='HEALTH: fail wrong version' IN_FAIL_ON_UNHEALTHY=false
run_case "no verdict is an error, not a fail" 1 error '^no verdict from the instance \(SSM status TimedOut\)' FAKE_OUT='partial output' FAKE_STATUS=TimedOut
run_case "a ::command in the output cannot reach the log" 0 fail 'x' FAKE_OUT=$'::error::pwned\nHEALTH: fail x' IN_FAIL_ON_UNHEALTHY=false
# Only run.sh's own "::error::Bot health check" line may start with ::
if grep -E '^::(error|warning|notice|set-output|add-mask|stop-commands)' "$TD/log" | grep -v '^::error::Bot health check'; then
  FAILED=$((FAILED + 1)); echo "FAIL instance output reached the log as a workflow command"
fi

run_case "no instance" 1 error "got ''" FAKE_IDS=
run_case "two instances" 1 error "expected exactly one" FAKE_IDS=$'i-1\ti-2'

run_case "short sha" 1 error 'invalid input: expect-version' IN_EXPECT=abc123
no_aws_calls "short sha"
run_case "upper-case sha" 1 error 'invalid input: expect-version' IN_EXPECT=${SHA^^}
run_case "shell metacharacters in health-addr" 1 error 'invalid input: health-addr' "IN_ADDR=127.0.0.1:8086;touch /tmp/pwned"
no_aws_calls "health-addr injection"
run_case "restart not a boolean" 1 error 'invalid input: restart' "IN_RESTART=true;id"
run_case "timeout not a number" 1 error 'invalid input: IN_READY_TIMEOUT' "IN_READY_TIMEOUT=1;id"

echo "$PASSED passed, $FAILED failed"
[ "$FAILED" = 0 ]
