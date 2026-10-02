#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
    echo "error: central agent command is not configured" >&2
    exit 2
fi

export LANG=C.UTF-8
export LC_ALL=C.UTF-8
export TERM="${TERM:-xterm-256color}"

stop_session() {
    tmux kill-server 2>/dev/null || true
    exit 0
}
trap stop_session TERM INT HUP

# Multiple command arguments are executed directly by tmux, without constructing
# a shell command. The config retains a dead pane so its exit status is available.
tmux -u -f /usr/local/share/wisp/central.tmux.conf new-session -d -s wisp -x 120 -y 40 -- "$@"
while tmux has-session -t wisp 2>/dev/null; do
    if [[ "$(tmux display-message -p -t wisp '#{pane_dead}')" == 1 ]]; then
        status="$(tmux display-message -p -t wisp '#{pane_dead_status}')"
        tmux kill-server
        exit "${status:-1}"
    fi
    sleep 1 &
    wait $! || true
done
echo "error: central terminal session disappeared" >&2
exit 1
