#!/bin/sh
# Do not enable xtrace: curl configuration carries an admin bearer credential.
set -eu
set +x
host=${ALLOWED_HOSTS%%,*}
[ -n "$host" ] || exit 1
# JSON quoting prevents curl-config injection and keeps the token out of argv.
auth=$(jq -er 'first(.[] | select(.role == "admin")) | "Authorization: Bearer \(.token)" | @json' /run/secrets/service_credentials) || exit 1
host_header=$(printf 'Host: %s' "$host" | jq -Rs .)
{
    printf 'header = %s\n' "$auth"
    printf 'header = %s\n' "$host_header"
} | curl --config - --silent --fail --output /dev/null --noproxy '*' \
    --connect-timeout 2 --max-time 5 http://127.0.0.1:8080/ready
