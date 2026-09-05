#!/usr/bin/env bash
set -euo pipefail

if [ -d /run/wisp/opencode/config ]; then
    mkdir -p -- "${HOME}/.config"
    ln -s -- /run/wisp/opencode/config "${HOME}/.config/opencode"
fi

if [ -f /run/wisp/opencode/auth.json ]; then
    mkdir -p -- "${HOME}/.local/share/opencode"
    ln -s -- /run/wisp/opencode/auth.json "${HOME}/.local/share/opencode/auth.json"
fi

if [ -n "${AWS_CONTAINER_CREDENTIALS_FULL_URI:-}" ]; then
    aws_configuration="$(
        curl \
            --fail \
            --silent \
            --show-error \
            --header "Authorization: ${AWS_CONTAINER_AUTHORIZATION_TOKEN}" \
            http://127.0.0.1:9911/configuration
    )"
    eks_cluster="$(jq -r '.eks_cluster // empty' <<< "${aws_configuration}")"

    if [ -n "${eks_cluster}" ]; then
        aws_alias="$(jq -er '.alias | select(type == "string" and length > 0)' <<< "${aws_configuration}")"
        aws_region="$(jq -er '.region | select(type == "string" and length > 0)' <<< "${aws_configuration}")"
        mkdir -p -- "$(dirname -- "${KUBECONFIG}")"
        chmod 700 -- "$(dirname -- "${KUBECONFIG}")"
        aws eks update-kubeconfig \
            --name "${eks_cluster}" \
            --region "${aws_region}" \
            --kubeconfig "${KUBECONFIG}" \
            --alias "${aws_alias}/${eks_cluster}" \
            --user-alias "${aws_alias}/${eks_cluster}"
        chmod 600 -- "${KUBECONFIG}"
    fi
fi

if (($# == 0)); then
    echo "error: sandbox command is not configured" >&2
    exit 2
fi

exec "$@"
