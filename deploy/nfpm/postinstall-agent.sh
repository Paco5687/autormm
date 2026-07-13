#!/bin/sh
# Runs after the autormm-agent package is installed. It reloads systemd but does
# NOT enable the service — the agent needs /etc/autormm/agent.env configured
# (server URL + enroll token) first.
set -e

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

if [ ! -f /etc/autormm/agent.env ]; then
    echo "autormm-agent installed. Next:"
    echo "  1. cp /etc/autormm/agent.env.example /etc/autormm/agent.env  (then edit it)"
    echo "  2. systemctl enable --now autormm-agent"
fi

exit 0
