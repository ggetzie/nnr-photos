#!/usr/bin/env bash
#
# photos-stack.sh - deploy the nnr-photos image optimizer for any project.
#
# Two phases, split by the permissions they need:
#
#   bootstrap   ADMIN. Creates S3 buckets and IAM execution roles.
#               Needs iam:CreateRole, iam:PutRolePolicy, iam:AttachRolePolicy,
#               s3:CreateBucket. Run once, by a human with elevated access.
#
#   deploy      DEPLOYER. Builds the binaries, creates/updates the functions,
#               wires the S3 triggers. Needs no IAM write access beyond
#               iam:PassRole on the two roles bootstrap created. Safe to run
#               repeatedly, from CI or an agent.
#
# Usage:
#   ./photos-stack.sh <command>
#
# Commands:
#   preflight         Check tools, credentials, and configuration
#   bootstrap         [ADMIN] Create buckets and IAM roles
#   deployer-policy   [ADMIN] Print the IAM policy a deployer identity needs
#   build             Build both deployment zips
#   deploy            Create or update both functions and wire the triggers
#   verify            End-to-end test with a generated image
#   status            Show what currently exists
#   destroy           [ADMIN] Tear everything down
#
# Configure with environment variables or a config file:
#   ./photos-stack.sh deploy                    # uses ./photos-stack.env if present
#   PROJECT=myapp SOURCE_BUCKET=... ./photos-stack.sh deploy

set -euo pipefail

# ---------------------------------------------------------------- configuration

CONFIG_FILE="${CONFIG_FILE:-$(dirname "$0")/photos-stack.env}"
# shellcheck disable=SC1090
[ -f "$CONFIG_FILE" ] && . "$CONFIG_FILE"

PROJECT="${PROJECT:-}"                  # required. Name prefix for all resources.
SOURCE_BUCKET="${SOURCE_BUCKET:-}"      # required. Where users upload.
DEST_BUCKET="${DEST_BUCKET:-}"          # required. Where derivatives land. MUST differ.
REGION="${REGION:-us-east-1}"
PROFILE="${PROFILE:-}"

ARCH="${ARCH:-arm64}"                   # arm64 is ~20% cheaper per GB-second
MEMORY="${MEMORY:-1769}"                # 1769 MB is where Lambda gives a full vCPU
TIMEOUT="${TIMEOUT:-60}"
CLEANUP_MEMORY="${CLEANUP_MEMORY:-512}"
CLEANUP_TIMEOUT="${CLEANUP_TIMEOUT:-30}"

# Optional image settings. Empty means the built-in defaults, which are the six
# CSS breakpoints No Nonsense Recipes uses. Set these for any other project.
DIMENSIONS="${DIMENSIONS:-}"            # "name:w,h;name:w,h"
FORMATS="${FORMATS:-}"                  # "jpeg,webp" or "jpeg,webp,png"
THUMB_SIZE="${THUMB_SIZE:-}"            # integer px

# Key prefix used by `verify`. It must fall inside whatever scope the execution
# roles allow: a role whose s3:DeleteObject is restricted to, say,
# "media/images/*" will deny a delete anywhere else, and verify will fail on the
# cleanup step even though the deployment is fine. Roles created by `bootstrap`
# cover the whole destination bucket, so the default works for new projects.
VERIFY_PREFIX="${VERIFY_PREFIX:-_photos-stack-verify}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Derived
FN_PHOTOS="${FN_PHOTOS:-${PROJECT}-photos}"
FN_CLEANUP="${FN_CLEANUP:-${PROJECT}-photos-cleanup}"
ROLE_PHOTOS="${ROLE_PHOTOS:-${PROJECT}-photos-role}"
ROLE_CLEANUP="${ROLE_CLEANUP:-${PROJECT}-photos-cleanup-role}"

# ---------------------------------------------------------------------- helpers

BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; RST=$'\033[0m'
[ -t 1 ] || { BOLD=""; DIM=""; RED=""; GRN=""; YEL=""; RST=""; }

say()  { printf '%s\n' "$*"; }
step() { printf '\n%s==>%s %s%s%s\n' "$GRN" "$RST" "$BOLD" "$*" "$RST"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '%s !! %s%s\n' "$YEL" "$*" "$RST" >&2; }
die()  { printf '%serror:%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

aws_() {
  local args=(--region "$REGION")
  [ -n "$PROFILE" ] && args+=(--profile "$PROFILE")
  command aws "$@" "${args[@]}"
}

# aws_ for global (non-regional) services still wants the profile
awsg() {
  local args=()
  [ -n "$PROFILE" ] && args+=(--profile "$PROFILE")
  command aws "$@" "${args[@]}"
}

require_config() {
  [ -n "$PROJECT" ]       || die "PROJECT is not set. See the header of this script."
  [ -n "$SOURCE_BUCKET" ] || die "SOURCE_BUCKET is not set."
  [ -n "$DEST_BUCKET" ]   || die "DEST_BUCKET is not set."
  [ "$SOURCE_BUCKET" != "$DEST_BUCKET" ] || \
    die "SOURCE_BUCKET and DEST_BUCKET must differ, or the ObjectCreated trigger will recurse into itself."
}

account_id() { awsg sts get-caller-identity --query Account --output text; }

fn_exists() { aws_ lambda get-function --function-name "$1" >/dev/null 2>&1; }
role_arn()  { awsg iam get-role --role-name "$1" --query 'Role.Arn' --output text 2>/dev/null || true; }

# Remove every object under a prefix. Used to tidy up after a failed verify so a
# botched run does not leave orphans behind.
purge_prefix() {
  local bucket="$1" prefix="$2" keys
  keys=$(aws_ s3api list-objects-v2 --bucket "$bucket" --prefix "$prefix" \
           --query 'Contents[].Key' --output text 2>/dev/null || true)
  [ -z "$keys" ] || [ "$keys" = "None" ] && return 0
  local n=0
  for k in $keys; do
    aws_ s3api delete-object --bucket "$bucket" --key "$k" >/dev/null 2>&1 && n=$((n + 1))
  done
  [ "$n" -gt 0 ] && info "cleaned up $n leftover object(s) under $bucket/$prefix"
  return 0
}

# Number of objects one upload produces: dims x formats, plus orig and thumbnail.
derivative_count() {
  local d f
  if [ -n "$DIMENSIONS" ]; then
    d=$(printf '%s' "$DIMENSIONS" | tr ';' '\n' | grep -c . || true)
  else
    d=6
  fi
  if [ -n "$FORMATS" ]; then
    f=$(printf '%s' "$FORMATS" | tr ',' '\n' | grep -c . || true)
  else
    f=2
  fi
  echo $(( d * f + 2 ))
}

# ------------------------------------------------------------------- preflight

cmd_preflight() {
  step "Checking tools"
  for t in aws jq go; do
    command -v "$t" >/dev/null 2>&1 || die "$t is required but not installed"
    info "$t: $(command -v "$t")"
  done
  command -v zip >/dev/null 2>&1 || info "zip: not found (the Makefile falls back to python3)"

  step "Checking credentials"
  local ident
  ident=$(awsg sts get-caller-identity --output json) || die "no usable AWS credentials"
  info "account: $(printf '%s' "$ident" | jq -r .Account)"
  info "identity: $(printf '%s' "$ident" | jq -r .Arn)"

  step "Configuration"
  require_config
  info "project           $PROJECT"
  info "region            $REGION"
  info "source bucket     $SOURCE_BUCKET"
  info "destination       $DEST_BUCKET"
  info "functions         $FN_PHOTOS, $FN_CLEANUP"
  info "roles             $ROLE_PHOTOS, $ROLE_CLEANUP"
  info "architecture      $ARCH"
  info "derivatives/image $(derivative_count)"
  info "verify prefix     $VERIFY_PREFIX"
  [ -n "$DIMENSIONS" ] && info "DIMENSIONS        $DIMENSIONS" || info "DIMENSIONS        (built-in defaults)"
  [ -n "$FORMATS" ]    && info "FORMATS           $FORMATS"    || info "FORMATS           (jpeg,webp)"
  [ -n "$THUMB_SIZE" ] && info "THUMB_SIZE        $THUMB_SIZE" || info "THUMB_SIZE        (128)"
  say ""
  say "${GRN}preflight passed${RST}"
}

# ------------------------------------------------------------ [ADMIN] bootstrap

make_bucket() {
  local b="$1"
  if awsg s3api head-bucket --bucket "$b" >/dev/null 2>&1; then
    info "bucket $b already exists"
    return
  fi
  if [ "$REGION" = "us-east-1" ]; then
    aws_ s3api create-bucket --bucket "$b" >/dev/null
  else
    aws_ s3api create-bucket --bucket "$b" \
      --create-bucket-configuration LocationConstraint="$REGION" >/dev/null
  fi
  info "created bucket $b"
}

make_role() {
  local name="$1" policy_name="$2" policy_doc="$3"
  local trust='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

  if [ -n "$(role_arn "$name")" ]; then
    info "role $name already exists, updating its inline policy"
  else
    awsg iam create-role --role-name "$name" \
      --description "Execution role for the $PROJECT photo optimizer" \
      --assume-role-policy-document "$trust" >/dev/null
    info "created role $name"
  fi

  awsg iam attach-role-policy --role-name "$name" \
    --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole >/dev/null
  awsg iam put-role-policy --role-name "$name" \
    --policy-name "$policy_name" --policy-document "$policy_doc" >/dev/null
  info "attached policies to $name"
}

cmd_bootstrap() {
  require_config
  step "Creating S3 buckets"
  make_bucket "$SOURCE_BUCKET"
  make_bucket "$DEST_BUCKET"

  step "Creating IAM execution roles"
  # The optimizer reads originals and writes derivatives. It never deletes.
  make_role "$ROLE_PHOTOS" "${PROJECT}-photos-s3" "$(cat <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "ReadOriginals",  "Effect": "Allow", "Action": ["s3:GetObject"],
      "Resource": "arn:aws:s3:::${SOURCE_BUCKET}/*" },
    { "Sid": "WriteDerivatives", "Effect": "Allow", "Action": ["s3:PutObject"],
      "Resource": "arn:aws:s3:::${DEST_BUCKET}/*" }
  ]
}
JSON
)"

  # The cleaner only ever lists and deletes, and only in the destination bucket.
  make_role "$ROLE_CLEANUP" "${PROJECT}-photos-cleanup-s3" "$(cat <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "ListDerivatives", "Effect": "Allow", "Action": ["s3:ListBucket"],
      "Resource": "arn:aws:s3:::${DEST_BUCKET}" },
    { "Sid": "DeleteDerivatives", "Effect": "Allow", "Action": ["s3:DeleteObject"],
      "Resource": "arn:aws:s3:::${DEST_BUCKET}/*" }
  ]
}
JSON
)"

  step "Done"
  info "photos role   $(role_arn "$ROLE_PHOTOS")"
  info "cleanup role  $(role_arn "$ROLE_CLEANUP")"
  say ""
  say "Next: ${BOLD}$0 deploy${RST}"
  say "${DIM}IAM role propagation can lag a few seconds; deploy retries automatically.${RST}"
}

# ------------------------------------------------- [ADMIN] deployer policy

cmd_deployer_policy() {
  require_config
  local acct; acct=$(account_id)
  # Roles may sit under a path (older console-created roles use /service-role/),
  # in which case the ARN is not simply role/<name>. Look up the real ARN when
  # we are allowed to, and fall back to the conventional form otherwise.
  local arn_photos arn_cleanup
  arn_photos=$(role_arn "$ROLE_PHOTOS"); arn_cleanup=$(role_arn "$ROLE_CLEANUP")
  [ -n "$arn_photos" ]  || arn_photos="arn:aws:iam::${acct}:role/${ROLE_PHOTOS}"
  [ -n "$arn_cleanup" ] || arn_cleanup="arn:aws:iam::${acct}:role/${ROLE_CLEANUP}"
  cat <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PassTheExecutionRolesToLambdaOnly",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": [
        "${arn_photos}",
        "${arn_cleanup}"
      ],
      "Condition": { "StringEquals": { "iam:PassedToService": "lambda.amazonaws.com" } }
    },
    {
      "Sid": "ManageTheseFunctions",
      "Effect": "Allow",
      "Action": [
        "lambda:CreateFunction", "lambda:DeleteFunction",
        "lambda:UpdateFunctionCode", "lambda:UpdateFunctionConfiguration",
        "lambda:GetFunction", "lambda:GetFunctionConfiguration",
        "lambda:InvokeFunction", "lambda:AddPermission", "lambda:RemovePermission",
        "lambda:GetPolicy", "lambda:TagResource", "lambda:ListTags"
      ],
      "Resource": [
        "arn:aws:lambda:${REGION}:${acct}:function:${FN_PHOTOS}",
        "arn:aws:lambda:${REGION}:${acct}:function:${FN_CLEANUP}"
      ]
    },
    {
      "Sid": "WireTheTrigger",
      "Effect": "Allow",
      "Action": ["s3:GetBucketNotification", "s3:PutBucketNotification"],
      "Resource": "arn:aws:s3:::${SOURCE_BUCKET}"
    },
    {
      "Sid": "VerifyEndToEnd",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": [
        "arn:aws:s3:::${SOURCE_BUCKET}/*",
        "arn:aws:s3:::${DEST_BUCKET}/*"
      ]
    },
    {
      "Sid": "ListForVerification",
      "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": [
        "arn:aws:s3:::${SOURCE_BUCKET}",
        "arn:aws:s3:::${DEST_BUCKET}"
      ]
    },
    {
      "Sid": "ReadLogs",
      "Effect": "Allow",
      "Action": ["logs:DescribeLogGroups", "logs:DescribeLogStreams",
                 "logs:GetLogEvents", "logs:FilterLogEvents"],
      "Resource": "arn:aws:logs:${REGION}:${acct}:log-group:/aws/lambda/${PROJECT}-photos*"
    }
  ]
}
JSON
}

# ----------------------------------------------------------------------- build

cmd_build() {
  step "Building deployment packages ($ARCH)"
  make -C "$REPO_ROOT" lambda ARCH="$ARCH" >/dev/null
  make -C "$REPO_ROOT" lambda-cleanup ARCH="$ARCH" >/dev/null
  info "optimizer $(du -h "$REPO_ROOT/photos-lambda.zip"  | cut -f1)  $REPO_ROOT/photos-lambda.zip"
  info "cleaner   $(du -h "$REPO_ROOT/cleanup-lambda.zip" | cut -f1)  $REPO_ROOT/cleanup-lambda.zip"
}

# ---------------------------------------------------------------------- deploy

# Create or update one function. IAM role propagation is eventually consistent,
# so CreateFunction is retried: a freshly created role often is not yet
# assumable by Lambda.
upsert_function() {
  local name="$1" role="$2" zip="$3" mem="$4" timeout="$5" env_json="$6"

  if fn_exists "$name"; then
    info "updating $name"
    aws_ lambda update-function-code --function-name "$name" \
      --zip-file "fileb://$zip" >/dev/null
    aws_ lambda wait function-updated --function-name "$name"
    aws_ lambda update-function-configuration --function-name "$name" \
      --role "$role" --handler bootstrap --runtime provided.al2023 \
      --memory-size "$mem" --timeout "$timeout" \
      --environment "$env_json" >/dev/null
    aws_ lambda wait function-updated --function-name "$name"
  else
    info "creating $name"
    local attempt=1
    until aws_ lambda create-function --function-name "$name" \
        --description "Photo optimizer for $PROJECT" \
        --package-type Zip --runtime provided.al2023 --handler bootstrap \
        --architectures "$ARCH" --role "$role" --zip-file "fileb://$zip" \
        --memory-size "$mem" --timeout "$timeout" \
        --environment "$env_json" >/dev/null 2>&1; do
      [ "$attempt" -ge 10 ] && die "create-function failed for $name after $attempt attempts"
      info "  role not assumable yet, retrying ($attempt/10)"
      attempt=$((attempt + 1))
      sleep 3
    done
    aws_ lambda wait function-active --function-name "$name"
  fi
}

# Allow S3 to invoke the function. Scoped to this bucket and this account so a
# bucket in someone else's account cannot trigger it.
allow_s3_invoke() {
  local name="$1" acct="$2"
  aws_ lambda remove-permission --function-name "$name" \
    --statement-id s3invoke >/dev/null 2>&1 || true
  aws_ lambda add-permission --function-name "$name" \
    --statement-id s3invoke --action lambda:InvokeFunction \
    --principal s3.amazonaws.com \
    --source-account "$acct" \
    --source-arn "arn:aws:s3:::${SOURCE_BUCKET}" >/dev/null
  info "allowed s3.amazonaws.com to invoke $name"
}

# PutBucketNotificationConfiguration REPLACES the entire configuration. Read the
# current one, drop only our own entries, add them back, and preserve every
# other trigger - topics, queues, EventBridge, and other people's functions.
wire_triggers() {
  local photos_arn="$1" cleanup_arn="$2"
  local current merged

  current=$(aws_ s3api get-bucket-notification-configuration --bucket "$SOURCE_BUCKET")

  merged=$(printf '%s' "$current" | jq \
    --arg p "$photos_arn" --arg c "$cleanup_arn" \
    --arg pid "${FN_PHOTOS}-ObjectCreated" --arg cid "${FN_CLEANUP}-ObjectRemoved" '
    def others: (.LambdaFunctionConfigurations // [])
      | map(select(.LambdaFunctionArn != $p and .LambdaFunctionArn != $c));
    {
      LambdaFunctionConfigurations: (others + [
        { Id: $pid, LambdaFunctionArn: $p, Events: ["s3:ObjectCreated:*"] },
        { Id: $cid, LambdaFunctionArn: $c, Events: ["s3:ObjectRemoved:*"] }
      ])
    }
    + (if .TopicConfigurations       then {TopicConfigurations: .TopicConfigurations}             else {} end)
    + (if .QueueConfigurations       then {QueueConfigurations: .QueueConfigurations}             else {} end)
    + (if .EventBridgeConfiguration  then {EventBridgeConfiguration: .EventBridgeConfiguration}   else {} end)
  ')

  local preserved
  preserved=$(printf '%s' "$current" | jq --arg p "$photos_arn" --arg c "$cleanup_arn" \
    '[(.LambdaFunctionConfigurations // [])[] | select(.LambdaFunctionArn != $p and .LambdaFunctionArn != $c)] | length')
  [ "$preserved" -gt 0 ] && info "preserving $preserved unrelated lambda trigger(s) on $SOURCE_BUCKET"

  aws_ s3api put-bucket-notification-configuration \
    --bucket "$SOURCE_BUCKET" --notification-configuration "$merged"
  info "wired ObjectCreated -> $FN_PHOTOS, ObjectRemoved -> $FN_CLEANUP"
}

cmd_deploy() {
  require_config
  local acct; acct=$(account_id)
  local rp rc
  rp=$(role_arn "$ROLE_PHOTOS"); rc=$(role_arn "$ROLE_CLEANUP")
  if [ -z "$rp" ] || [ -z "$rc" ]; then
    # A deployer without iam:GetRole cannot look these up. Fall back to the
    # conventional ARNs, which is what bootstrap creates.
    rp="arn:aws:iam::${acct}:role/${ROLE_PHOTOS}"
    rc="arn:aws:iam::${acct}:role/${ROLE_CLEANUP}"
    warn "cannot read roles via iam:GetRole, assuming $rp and $rc"
  fi

  [ -f "$REPO_ROOT/photos-lambda.zip" ]  || cmd_build
  [ -f "$REPO_ROOT/cleanup-lambda.zip" ] || cmd_build

  # Environment for the optimizer. Only DESTINATION_BUCKET is required; the
  # others fall back to built-in defaults when unset.
  local envmap
  envmap=$(jq -nc --arg d "$DEST_BUCKET" --arg dim "$DIMENSIONS" \
                  --arg f "$FORMATS" --arg t "$THUMB_SIZE" '
    {Variables: ({DESTINATION_BUCKET: $d}
      + (if $dim != "" then {DIMENSIONS: $dim} else {} end)
      + (if $f   != "" then {FORMATS: $f}      else {} end)
      + (if $t   != "" then {THUMB_SIZE: $t}   else {} end))}')

  # The cleaner deletes one ListObjectsV2 page and does not paginate, so
  # MAX_KEYS must cover a whole derivative set or folders are cleaned partially.
  local maxkeys; maxkeys=$(derivative_count)
  local cleanup_envmap
  cleanup_envmap=$(jq -nc --arg d "$DEST_BUCKET" --arg m "$maxkeys" \
    '{Variables: {DESTINATION_BUCKET: $d, MAX_KEYS: $m}}')

  step "Deploying functions"
  upsert_function "$FN_PHOTOS"  "$rp" "$REPO_ROOT/photos-lambda.zip"  "$MEMORY" "$TIMEOUT" "$envmap"
  upsert_function "$FN_CLEANUP" "$rc" "$REPO_ROOT/cleanup-lambda.zip" "$CLEANUP_MEMORY" "$CLEANUP_TIMEOUT" "$cleanup_envmap"
  info "MAX_KEYS set to $maxkeys ($(derivative_count) derivatives per image)"

  step "Granting S3 invoke permission"
  allow_s3_invoke "$FN_PHOTOS"  "$acct"
  allow_s3_invoke "$FN_CLEANUP" "$acct"

  step "Wiring bucket notifications"
  wire_triggers \
    "arn:aws:lambda:${REGION}:${acct}:function:${FN_PHOTOS}" \
    "arn:aws:lambda:${REGION}:${acct}:function:${FN_CLEANUP}"

  step "Deployed"
  say "Run ${BOLD}$0 verify${RST} to test the pipeline end to end."
}

# ---------------------------------------------------------------------- verify

cmd_verify() {
  require_config
  local prefix="${VERIFY_PREFIX%/}/$$"
  local want; want=$(derivative_count)
  local tmp; tmp=$(mktemp -d)
  # Expand $tmp now, not at trap time: it is a local and would be out of scope
  # when the trap fires, which trips `set -u`.
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  step "Generating a test image"
  # A real JPEG, produced without depending on ImageMagick being installed.
  ( cd "$REPO_ROOT" && cat > "$tmp/gen.go" <<'GO'
package main

import ("image";"image/color";"image/jpeg";"math";"os")

func main() {
	const W, H = 1600, 1200
	m := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			fx, fy := float64(x)/W, float64(y)/H
			cl := func(f float64) uint8 {
				if f < 0 { return 0 }
				if f > 255 { return 255 }
				return uint8(f)
			}
			m.Set(x, y, color.RGBA{
				cl(128 + 110*math.Sin(fx*12)*math.Cos(fy*8)),
				cl(128 + 110*math.Sin((fx+fy)*9)),
				cl(220 * fy), 255})
		}
	}
	f, _ := os.Create(os.Args[1])
	defer f.Close()
	jpeg.Encode(f, m, &jpeg.Options{Quality: 88})
}
GO
    go run "$tmp/gen.go" "$tmp/test.jpg" )
  info "$(du -h "$tmp/test.jpg" | cut -f1) 1600x1200 JPEG"

  step "Uploading to s3://$SOURCE_BUCKET/$prefix/orig.jpeg"
  aws_ s3api put-object --bucket "$SOURCE_BUCKET" --key "$prefix/orig.jpeg" \
    --body "$tmp/test.jpg" --content-type image/jpeg >/dev/null

  step "Waiting for derivatives (expecting $want)"
  local n=0 waited=0
  while [ "$waited" -lt 90 ]; do
    n=$(aws_ s3api list-objects-v2 --bucket "$DEST_BUCKET" --prefix "$prefix/" \
          --query 'length(Contents)' --output text 2>/dev/null || echo 0)
    [ "$n" = "None" ] && n=0
    [ "$n" -ge "$want" ] && break
    sleep 5; waited=$((waited + 5))
    printf '    %ss: %s/%s\r' "$waited" "$n" "$want"
  done
  say ""
  if [ "$n" -lt "$want" ]; then
    warn "got $n of $want derivatives after ${waited}s"
    warn "check logs: aws logs tail /aws/lambda/$FN_PHOTOS --since 5m"
    aws_ s3api delete-object --bucket "$SOURCE_BUCKET" --key "$prefix/orig.jpeg" >/dev/null 2>&1 || true
    purge_prefix "$DEST_BUCKET" "$prefix/"
    die "verification failed"
  fi
  info "$n derivatives created"
  aws_ s3api list-objects-v2 --bucket "$DEST_BUCKET" --prefix "$prefix/" \
    --query 'Contents[].[Key,Size]' --output text | \
    while read -r k s; do printf '      %-42s %8s B\n' "${k##*/}" "$s"; done

  step "Checking Content-Type and Cache-Control"
  local ct cc
  ct=$(aws_ s3api head-object --bucket "$DEST_BUCKET" --key "$prefix/orig.jpeg" --query ContentType --output text)
  cc=$(aws_ s3api head-object --bucket "$DEST_BUCKET" --key "$prefix/orig.jpeg" --query CacheControl --output text)
  info "orig.jpeg  Content-Type: $ct  Cache-Control: $cc"
  [ "$ct" = "image/jpeg" ] || warn "unexpected Content-Type: $ct"

  step "Deleting the original to test the cleaner"
  aws_ s3api delete-object --bucket "$SOURCE_BUCKET" --key "$prefix/orig.jpeg" >/dev/null
  waited=0
  while [ "$waited" -lt 60 ]; do
    n=$(aws_ s3api list-objects-v2 --bucket "$DEST_BUCKET" --prefix "$prefix/" \
          --query 'length(Contents)' --output text 2>/dev/null || echo 0)
    [ "$n" = "None" ] && n=0
    [ "$n" -eq 0 ] && break
    sleep 5; waited=$((waited + 5))
    printf '    %ss: %s remaining\r' "$waited" "$n"
  done
  say ""
  if [ "$n" -ne 0 ]; then
    warn "$n derivatives still present after ${waited}s"
    warn "Two things cause this:"
    warn "  1. VERIFY_PREFIX ($VERIFY_PREFIX) is outside the scope the cleanup"
    warn "     role allows s3:DeleteObject on. Check the log for AccessDenied."
    warn "  2. MAX_KEYS is lower than $want, so only part of the set is removed."
    warn "  aws logs tail /aws/lambda/$FN_CLEANUP --since 5m"
    purge_prefix "$DEST_BUCKET" "$prefix/"
    die "cleanup verification failed"
  fi
  info "all derivatives removed"

  step "Verified"
  say "${GRN}The pipeline works end to end.${RST}"
}

# ---------------------------------------------------------------------- status

cmd_status() {
  require_config
  local acct; acct=$(account_id)
  step "Functions"
  printf '    %-24s %-17s %-6s %7s %7s %10s %s\n' \
    NAME RUNTIME ARCH MEMORY TIMEOUT SIZE STATE
  for f in "$FN_PHOTOS" "$FN_CLEANUP"; do
    if fn_exists "$f"; then
      aws_ lambda get-function-configuration --function-name "$f" \
        --query '[FunctionName,Runtime,Architectures[0],MemorySize,Timeout,CodeSize,State]' \
        --output json | jq -r '@tsv' | \
        while IFS=$'\t' read -r n rt ar mem to sz st; do
          printf '    %-24s %-17s %-6s %4s MB %6ss %9s B %s\n' \
            "$n" "$rt" "$ar" "$mem" "$to" "$sz" "$st"
        done
    else
      printf '    %-24s %s\n' "$f" "(does not exist)"
    fi
  done

  step "Triggers on $SOURCE_BUCKET"
  aws_ s3api get-bucket-notification-configuration --bucket "$SOURCE_BUCKET" | \
    jq -r '(.LambdaFunctionConfigurations // [])[] |
           "    \(.LambdaFunctionArn | split(":") | last)  \(.Events | join(", "))"'

  step "Roles"
  for r in "$ROLE_PHOTOS" "$ROLE_CLEANUP"; do
    local a; a=$(role_arn "$r")
    info "${a:-$r  ${DIM}(not readable or does not exist)${RST}}"
  done
}

# --------------------------------------------------------------- [ADMIN] destroy

cmd_destroy() {
  require_config
  local acct; acct=$(account_id)
  warn "This deletes the functions, the roles, and this stack's bucket triggers."
  warn "It does NOT delete your buckets or any images in them."
  printf 'Type the project name (%s) to continue: ' "$PROJECT"
  local answer; read -r answer
  [ "$answer" = "$PROJECT" ] || die "aborted"

  step "Removing bucket triggers"
  local current merged
  current=$(aws_ s3api get-bucket-notification-configuration --bucket "$SOURCE_BUCKET")
  merged=$(printf '%s' "$current" | jq \
    --arg p "arn:aws:lambda:${REGION}:${acct}:function:${FN_PHOTOS}" \
    --arg c "arn:aws:lambda:${REGION}:${acct}:function:${FN_CLEANUP}" '
    { LambdaFunctionConfigurations: ((.LambdaFunctionConfigurations // [])
        | map(select(.LambdaFunctionArn != $p and .LambdaFunctionArn != $c))) }
    + (if .TopicConfigurations      then {TopicConfigurations: .TopicConfigurations}           else {} end)
    + (if .QueueConfigurations      then {QueueConfigurations: .QueueConfigurations}           else {} end)
    + (if .EventBridgeConfiguration then {EventBridgeConfiguration: .EventBridgeConfiguration} else {} end)')
  aws_ s3api put-bucket-notification-configuration --bucket "$SOURCE_BUCKET" \
    --notification-configuration "$merged"
  info "removed"

  step "Deleting functions"
  for f in "$FN_PHOTOS" "$FN_CLEANUP"; do
    fn_exists "$f" && { aws_ lambda delete-function --function-name "$f"; info "deleted $f"; } || info "$f absent"
  done

  step "Deleting roles"
  for r in "$ROLE_PHOTOS:${PROJECT}-photos-s3" "$ROLE_CLEANUP:${PROJECT}-photos-cleanup-s3"; do
    local name="${r%%:*}" pol="${r##*:}"
    [ -z "$(role_arn "$name")" ] && { info "$name absent"; continue; }
    awsg iam delete-role-policy --role-name "$name" --policy-name "$pol" >/dev/null 2>&1 || true
    awsg iam detach-role-policy --role-name "$name" \
      --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole >/dev/null 2>&1 || true
    awsg iam delete-role --role-name "$name" >/dev/null && info "deleted $name"
  done

  step "Destroyed"
}

# ------------------------------------------------------------------------- main

usage() { sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//; $d'; }

case "${1:-}" in
  preflight)        cmd_preflight ;;
  bootstrap)        cmd_bootstrap ;;
  deployer-policy)  cmd_deployer_policy ;;
  build)            cmd_build ;;
  deploy)           cmd_deploy ;;
  verify)           cmd_verify ;;
  status)           cmd_status ;;
  destroy)          cmd_destroy ;;
  ""|-h|--help|help) usage ;;
  *) die "unknown command: $1 (try --help)" ;;
esac
