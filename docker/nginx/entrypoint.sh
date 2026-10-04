#!/bin/sh
set -eu
set +x
# Only DNS authorities are accepted. Never interpolate arbitrary config text.
if ! printf '%s\n' "$ALLOWED_HOSTS" | awk -F, '
function valid(host, labels, count, i, parts) {
    if (host == "") return 0
    count = split(host, parts, ":")
    if (length(parts[1]) > 253) return 0
    if (count > 2 || (count == 2 && (parts[2] !~ /^[0-9]+$/ || parts[2] < 1 || parts[2] > 65535))) return 0
    if (parts[1] ~ /^[0-9.]+$/) return 0
    count = split(parts[1], labels, ".")
    for (i = 1; i <= count; i++) {
        if (length(labels[i]) > 63 || labels[i] !~ /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/) return 0
    }
    return 1
}
{ if (NR != 1 || NF == 0) exit 1; for (i = 1; i <= NF; i++) if (!valid($i)) exit 1 }
'; then
    printf '%s\n' 'Invalid ALLOWED_HOSTS' >&2
    exit 1
fi
NGINX_HOST_MAP=$(printf '%s\n' "$ALLOWED_HOSTS" | awk -F, '{for(i=1;i<=NF;i++) printf "    \"%s\" 1;\n", $i}')
NGINX_SERVER_NAMES=$(printf '%s\n' "$ALLOWED_HOSTS" | tr ',' '\n' | cut -d: -f1 | sort -u | tr '\n' ' ')
export NGINX_HOST_MAP NGINX_SERVER_NAMES
# Limit substitution to our own variables; preserve nginx $http_host etc.
envsubst '${NGINX_HOST_MAP} ${NGINX_SERVER_NAMES}' \
    < /etc/yexjudge/nginx.conf.template > /tmp/nginx.conf
nginx -t -q -c /tmp/nginx.conf
exec nginx -c /tmp/nginx.conf -g 'daemon off;'
