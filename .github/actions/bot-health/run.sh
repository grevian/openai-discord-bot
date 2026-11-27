#!/usr/bin/env bash
# Runner side of the bot-health action: validates the inputs, sends check.sh to
# the instance with SSM Run Command, waits for it, and turns its last line
# (HEALTH: ok|fail <reason>) into the result/reason outputs and a job summary.
set -uo pipefail

: "${GITHUB_OUTPUT:?}" "${GITHUB_STEP_SUMMARY:?}" "${CHECK_SCRIPT:?}"
ERR=$(mktemp)
trap 'rm -f "$ERR"' EXIT
OUTPUT_TEXT=
SSM_STATUS=-

# finish ok|fail|error <reason>: set the outputs, write the summary, exit.
finish() {
  local result=$1 reason
  reason=$(printf '%s' "$2" | tr -cd '[:print:]' | cut -c1-300)
  {
    echo "result=$result"
    echo "reason=$reason"
  } >> "$GITHUB_OUTPUT"
  {
    echo "### Bot health check: $result"
    echo
    echo "- expected release: \`${IN_EXPECT:0:12}\`"
    echo "- restart: \`${IN_RESTART:-?}\`, SSM status: \`$SSM_STATUS\`"
    [ -n "$reason" ] && echo "- reason: $reason"
    if [ -n "$OUTPUT_TEXT" ]; then
      echo
      echo '```'
      printf '%s\n' "$OUTPUT_TEXT" | tr -d '`'
      echo '```'
    fi
  } >> "$GITHUB_STEP_SUMMARY"
  if [ "$result" = ok ]; then
    echo "Bot health check: ok${reason:+ ($reason)}"
    exit 0
  fi
  echo "::error::Bot health check $result: $reason"
  [ "${IN_FAIL_ON_UNHEALTHY:-true}" = false ] && exit 0
  exit 1
}

# 1. Validate every input: they are embedded in the command run as root.
is_int() { [[ $1 =~ ^[0-9]{1,5}$ ]]; }
[[ ${IN_EXPECT:-} =~ ^[0-9a-f]{40}$ ]] || finish error "invalid input: expect-version must be a 40 character lowercase hex sha"
[[ ${IN_RESTART:-} =~ ^(true|false)$ ]] || finish error "invalid input: restart must be true or false"
[[ ${IN_ALLOW_NO_ENDPOINT:-} =~ ^(true|false)$ ]] || finish error "invalid input: allow-no-endpoint must be true or false"
[[ ${IN_FAIL_ON_UNHEALTHY:-} =~ ^(true|false)$ ]] || finish error "invalid input: fail-on-unhealthy must be true or false"
[[ ${IN_ADDR:-} =~ ^[0-9a-zA-Z.:-]{1,64}$ ]] || finish error "invalid input: health-addr must be host:port"
for v in IN_READY_TIMEOUT IN_STABLE IN_INSTANCE_WAIT; do
  is_int "${!v:-}" || finish error "invalid input: $v must be a whole number of seconds"
done

# 2. Exactly one running instance, as instance-status.yml does. While a
# refreshed instance is still coming up, keep looking for instance-wait seconds.
INSTANCE_ID=
LOOKUP_DEADLINE=$((SECONDS + IN_INSTANCE_WAIT))
while :; do
  if IDS=$(aws ec2 describe-instances \
    --filters "Name=tag:App,Values=openai-discord-bot" "Name=instance-state-name,Values=running" \
    --query "Reservations[].Instances[].InstanceId" --output text 2> "$ERR"); then
    if [[ -n $IDS && $IDS != *[[:space:]]* ]]; then INSTANCE_ID=$IDS; break; fi
    WHY="expected exactly one running instance, got '${IDS//[$'\t\n']/ }'"
  else
    WHY="describe-instances failed: $(head -c 200 "$ERR")"
  fi
  (( SECONDS >= LOOKUP_DEADLINE )) && finish error "$WHY"
  sleep 15
done
echo "Instance: $INSTANCE_ID"

# 3. The command: a VAR=value block, then check.sh, one line per array element.
EXEC_TIMEOUT=$((IN_READY_TIMEOUT + IN_STABLE + 600))
PARAMS=$(jq -nc \
  --arg pre "EXPECT='$IN_EXPECT'
RESTART='$IN_RESTART'
READY_TIMEOUT='$IN_READY_TIMEOUT'
STABLE='$IN_STABLE'
ALLOW_NO_ENDPOINT='$IN_ALLOW_NO_ENDPOINT'
ADDR='$IN_ADDR'
export EXPECT RESTART READY_TIMEOUT STABLE ALLOW_NO_ENDPOINT ADDR" \
  --rawfile script "$CHECK_SCRIPT" \
  --arg timeout "$EXEC_TIMEOUT" \
  '{commands: (($pre + "\n" + $script) | split("\n")), executionTimeout: [$timeout]}') \
  || finish error "could not build the SSM parameters"

# 4. Send it; a fresh instance can take a while to register with SSM.
SEND_DEADLINE=$((SECONDS + IN_INSTANCE_WAIT))
while :; do
  if CMD_ID=$(aws ssm send-command --document-name AWS-RunShellScript \
    --instance-ids "$INSTANCE_ID" --timeout-seconds 600 \
    --comment "Bot health ${IN_EXPECT:0:12}" \
    --parameters "$PARAMS" --query Command.CommandId --output text 2> "$ERR"); then
    break
  fi
  if grep -q InvalidInstanceId "$ERR" && (( SECONDS < SEND_DEADLINE )); then
    echo "instance $INSTANCE_ID is not registered with SSM yet; retrying"
    sleep 15
    continue
  fi
  finish error "send-command failed: $(head -c 200 "$ERR")"
done
echo "Command: $CMD_ID (timeout ${EXEC_TIMEOUT}s)"

# 5. Wait. Not `aws ssm wait command-executed`: its waiter gives up after 100 s.
WAIT_DEADLINE=$((SECONDS + EXEC_TIMEOUT + 120))
MISSING=0
while :; do
  if SSM_STATUS=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query Status --output text 2> "$ERR"); then
    MISSING=0
    case $SSM_STATUS in Pending | InProgress | Delayed) ;; *) break ;; esac
  else
    SSM_STATUS=-
    # InvocationDoesNotExist is normal for a few seconds after send-command
    MISSING=$((MISSING + 1))
    (( MISSING >= 12 )) && finish error "get-command-invocation keeps failing: $(head -c 200 "$ERR")"
  fi
  (( SECONDS >= WAIT_DEADLINE )) && finish error "the check did not finish in $((EXEC_TIMEOUT + 120))s"
  sleep 5
done

# 6. The output. Lines are prefixed so nothing in it can act as a workflow command.
if RESULT_JSON=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query '{out: StandardOutputContent, err: StandardErrorContent}' --output json 2> "$ERR"); then
  OUTPUT_TEXT=$(jq -r '.out' <<< "$RESULT_JSON" | tr -cd '[:print:]\n' | cut -c1-300)
  ERR_TEXT=$(jq -r '.err' <<< "$RESULT_JSON" | tr -cd '[:print:]\n' | cut -c1-300 | tail -n 5)
else
  ERR_TEXT="could not read the output: $(head -c 200 "$ERR")"
fi
echo "::group::Instance output (SSM status $SSM_STATUS)"
printf '%s\n' "$OUTPUT_TEXT" | sed 's/^/| /'
[ -n "${ERR_TEXT:-}" ] && printf '%s\n' "$ERR_TEXT" | sed 's/^/! /'
echo "::endgroup::"

LINE=$(printf '%s\n' "$OUTPUT_TEXT" | grep -a '^HEALTH: ' | tail -n 1)
case $LINE in
  "HEALTH: ok") finish ok "" ;;
  "HEALTH: ok "*) finish ok "${LINE#HEALTH: ok }" ;;
  "HEALTH: fail "*) finish fail "${LINE#HEALTH: fail }" ;;
  *) finish error "no verdict from the instance (SSM status $SSM_STATUS)" ;;
esac
