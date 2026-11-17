#!/bin/sh
# Foreground launcher and exact managed-kernel cleanup for an isolated T02 host.
set -eu
umask 077
if [ "$#" -lt 2 ]; then
    echo 'usage: stage2-host.sh /tmp/meshstrata-t02-<run> verify|controller|agent|stop-kernel|audit [action]' >&2
    exit 2
fi
root=$1
mode=$2
case "$root" in
    /tmp/meshstrata-t02-*/*|*/../*|*/./*) exit 2 ;;
    /tmp/meshstrata-t02-?*) ;;
    *) exit 2 ;;
esac
if [ -L "$root" ] || [ "$(stat -c '%u:%a' "$root")" != "$(id -u):700" ]; then
    echo 'test root must be an owned 0700 directory' >&2
    exit 1
fi
bin="$root/artifacts"
export GOMAXPROCS=1
if [ "$mode" = verify ]; then
    (cd "$bin" && sha256sum -c SHA256SUMS)
    (cd "$bin/test" && sha256sum -c SHA256SUMS)
    exit 0
fi
if [ "$mode" = audit ]; then
    leftovers=0
    for entry in /proc/[0-9]*/exe; do
        exe=$(readlink "$entry" 2>/dev/null || true)
        case "$exe" in
            "$bin/"*)
                printf '%s %s\n' "$entry" "${exe#"$root/"}"
                leftovers=1
                ;;
        esac
    done
    # Socket paths are recorded even when they are stale after SIGKILL.
    find "$root" -type s -print
    exit "$leftovers"
fi
action=${3:-run}
case "$mode:$action" in
    controller:run|controller:prepare-local|controller:initialize-admin|controller:submit-release)
        exec "$bin/meshstrata-controller" "$action" -config "$root/controller.json"
        ;;
    agent:run|agent:enroll|agent:prepare-runtime|agent:healthcheck)
        exec "$bin/meshstrata-agent" "$action" -config "$root/agent.json"
        ;;
    stop-kernel:run)
        exec "$bin/meshstrata-agent" stop-runtime -config "$root/agent.json"
        ;;
    *)
        echo 'unsupported isolated host action' >&2
        exit 2
        ;;
esac
