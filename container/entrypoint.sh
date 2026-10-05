#!/usr/bin/env bash
set -euo pipefail

startup_timing() {
    if [ "${WISP_STARTUP_TIMING:-}" = "1" ]; then
        printf '[wisp startup] %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" "$1" >&2
    fi
}

startup_timing container.entrypoint.start

if [ -d /run/wisp/opencode/config ]; then
    mkdir -p -- "${HOME}/.config"
    ln -s -- /run/wisp/opencode/config "${HOME}/.config/opencode"
fi

# Load the dependency-free reporter directly, without adding a config-discovery
# directory (OpenCode installs directory dependencies during startup).
export OPENCODE_CONFIG=/usr/local/share/wisp/opencode.json

if [ -f /run/wisp/hunk/config.toml ]; then
    mkdir -p -- "${HOME}/.config/hunk"
    ln -s -- /run/wisp/hunk/config.toml "${HOME}/.config/hunk/config.toml"
fi

if [ -d /run/wisp/jiti-cache ]; then
    if [[ ! "${WISP_PI_JITI_CACHE_KEY:-}" =~ ^[0-9a-f]{64}$ ]]; then
        echo "error: invalid Pi Jiti cache key" >&2
        exit 2
    fi
    jiti_cache="/run/wisp/jiti-cache/${WISP_PI_JITI_CACHE_KEY}"
    if [ ! -d "${jiti_cache}" ] || [ -L "${jiti_cache}" ]; then
        echo "error: invalid Pi Jiti cache directory" >&2
        exit 2
    fi
    ln -s -- "${jiti_cache}" "${TMPDIR}/jiti"
fi

startup_timing container.setup.ready

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

# Azure credentials are explicitly scoped to Wisp, not application SDK inputs.
azure_count=0
for azure_key in WISP_AZURE_CLIENT_ID WISP_AZURE_TENANT_ID WISP_AZURE_SUBSCRIPTION_ID WISP_AZURE_CLIENT_SECRET; do
    if [ -n "${!azure_key:-}" ]; then
        azure_count=$((azure_count + 1))
    fi
done
if ((azure_count != 0)); then
    if ((azure_count != 4)); then
        echo "error: Azure login requires all four WISP_AZURE_* credential variables" >&2
        exit 2
    fi
    # Never reuse a host token cache or print authentication command output.
    export AZURE_CONFIG_DIR="${HOME}/.azure"
    mkdir -p -- "${AZURE_CONFIG_DIR}"
    chmod 700 -- "${AZURE_CONFIG_DIR}"
    if ! az login --service-principal \
        --username "${WISP_AZURE_CLIENT_ID}" \
        --password="${WISP_AZURE_CLIENT_SECRET}" \
        --tenant "${WISP_AZURE_TENANT_ID}" \
        --output none >/dev/null 2>&1; then
        echo "error: Azure service-principal login failed" >&2
        exit 1
    fi
    if ! az account set --subscription "${WISP_AZURE_SUBSCRIPTION_ID}" >/dev/null 2>&1; then
        echo "error: Azure subscription selection failed" >&2
        exit 1
    fi
fi

startup_timing container.bootstrap.ready

if (($# == 0)); then
    echo "error: sandbox command is not configured" >&2
    exit 2
fi

startup_timing container.exec
exec "$@"
