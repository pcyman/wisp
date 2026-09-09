FROM debian:bookworm-slim

ARG TARGETARCH
ARG OPENCODE_VERSION=""
ARG HUNK_VERSION=""
ARG AWS_CLI_VERSION=""
ARG KUBECTL_VERSION=""
ARG HELM_VERSION=""
ARG TERRAFORM_VERSION=""
ARG GO_VERSION=""

RUN apt-get update \
    && apt-get install --no-install-recommends -y \
        bash \
        ca-certificates \
        curl \
        gcc \
        git \
        gzip \
        jq \
        libc6-dev \
        nodejs \
        npm \
        python3 \
        python-is-python3 \
        ripgrep \
        unzip \
    && rm -rf /var/lib/apt/lists/*

# OpenCode's official installer selects the correct standalone binary for the
# build architecture. Pin it with --build-arg OPENCODE_VERSION=x.y.z if needed.
RUN set -eux; \
    curl -fsSL https://opencode.ai/install -o /tmp/install-opencode; \
    if [ -n "${OPENCODE_VERSION}" ]; then \
        HOME=/tmp/opencode-home bash /tmp/install-opencode --version "${OPENCODE_VERSION}" --no-modify-path; \
    else \
        HOME=/tmp/opencode-home bash /tmp/install-opencode --no-modify-path; \
    fi; \
    install -m 0755 /tmp/opencode-home/.opencode/bin/opencode /usr/local/bin/opencode; \
    rm -rf /tmp/install-opencode /tmp/opencode-home

RUN set -eux; \
    if [ -n "${HUNK_VERSION}" ]; then \
        npm install --global "hunkdiff@${HUNK_VERSION}"; \
    else \
        npm install --global hunkdiff; \
    fi; \
    npm cache clean --force

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) arch="x86_64" ;; \
        arm64) arch="aarch64" ;; \
        *) echo "Unsupported architecture for AWS CLI: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    if [ -n "${AWS_CLI_VERSION}" ]; then \
        archive="awscli-exe-linux-${arch}-${AWS_CLI_VERSION}.zip"; \
    else \
        archive="awscli-exe-linux-${arch}.zip"; \
    fi; \
    curl -fsSLo /tmp/awscliv2.zip "https://awscli.amazonaws.com/${archive}"; \
    unzip -q /tmp/awscliv2.zip -d /tmp; \
    /tmp/aws/install --bin-dir /usr/local/bin --install-dir /usr/local/aws-cli; \
    rm -rf /tmp/aws /tmp/awscliv2.zip

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64|arm64) arch="${TARGETARCH}" ;; \
        *) echo "Unsupported architecture for kubectl: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    version="${KUBECTL_VERSION}"; \
    if [ -z "${version}" ]; then version="$(curl -fsSL https://dl.k8s.io/release/stable.txt)"; fi; \
    curl -fsSLo /tmp/kubectl "https://dl.k8s.io/release/${version}/bin/linux/${arch}/kubectl"; \
    curl -fsSLo /tmp/kubectl.sha256 "https://dl.k8s.io/release/${version}/bin/linux/${arch}/kubectl.sha256"; \
    echo "$(cat /tmp/kubectl.sha256)  /tmp/kubectl" | sha256sum --check; \
    install -m 0755 /tmp/kubectl /usr/local/bin/kubectl; \
    rm /tmp/kubectl /tmp/kubectl.sha256

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64|arm64) arch="${TARGETARCH}" ;; \
        *) echo "Unsupported architecture for Helm: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    version="${HELM_VERSION}"; \
    if [ -z "${version}" ]; then version="$(curl -fsSL https://get.helm.sh/helm3-latest-version)"; fi; \
    archive="helm-${version}-linux-${arch}.tar.gz"; \
    curl -fsSLo "/tmp/${archive}" "https://get.helm.sh/${archive}"; \
    curl -fsSLo "/tmp/${archive}.sha256sum" "https://get.helm.sh/${archive}.sha256sum"; \
    (cd /tmp && sha256sum --check "${archive}.sha256sum"); \
    tar -xzf "/tmp/${archive}" -C /tmp; \
    install -m 0755 "/tmp/linux-${arch}/helm" /usr/local/bin/helm; \
    rm -rf "/tmp/${archive}" "/tmp/${archive}.sha256sum" "/tmp/linux-${arch}"

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64|arm64) arch="${TARGETARCH}" ;; \
        *) echo "Unsupported architecture for Terraform: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    version="${TERRAFORM_VERSION}"; \
    if [ -z "${version}" ]; then \
        version="$(curl -fsSL https://checkpoint-api.hashicorp.com/v1/check/terraform | jq -r .current_version)"; \
    fi; \
    version="${version#v}"; \
    archive="terraform_${version}_linux_${arch}.zip"; \
    base_url="https://releases.hashicorp.com/terraform/${version}"; \
    curl -fsSLo "/tmp/${archive}" "${base_url}/${archive}"; \
    curl -fsSLo /tmp/terraform_SHA256SUMS "${base_url}/terraform_${version}_SHA256SUMS"; \
    checksum="$(awk -v archive="${archive}" '$2 == archive { print $1 }' /tmp/terraform_SHA256SUMS)"; \
    if [ -z "${checksum}" ]; then echo "Terraform checksum not found: ${archive}" >&2; exit 1; fi; \
    echo "${checksum}  /tmp/${archive}" | sha256sum --check; \
    unzip -q "/tmp/${archive}" -d /tmp/terraform; \
    install -m 0755 /tmp/terraform/terraform /usr/local/bin/terraform; \
    rm -rf "/tmp/${archive}" /tmp/terraform /tmp/terraform_SHA256SUMS

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64|arm64) arch="${TARGETARCH}" ;; \
        *) echo "Unsupported architecture for Go: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o /tmp/go-releases.json; \
    version="${GO_VERSION}"; \
    if [ -z "${version}" ]; then \
        version="$(jq -r 'map(select(.stable))[0].version' /tmp/go-releases.json)"; \
    fi; \
    case "${version}" in go*) ;; *) version="go${version}" ;; esac; \
    archive="${version}.linux-${arch}.tar.gz"; \
    checksum="$(jq -r --arg version "${version}" --arg archive "${archive}" \
        '.[] | select(.version == $version) | .files[] | select(.filename == $archive) | .sha256' \
        /tmp/go-releases.json)"; \
    if [ -z "${checksum}" ] || [ "${checksum}" = null ]; then \
        echo "Go release not found: ${archive}" >&2; exit 1; \
    fi; \
    curl -fsSLo "/tmp/${archive}" "https://go.dev/dl/${archive}"; \
    echo "${checksum}  /tmp/${archive}" | sha256sum --check; \
    tar -xzf "/tmp/${archive}" -C /usr/local; \
    rm "/tmp/${archive}" /tmp/go-releases.json

RUN groupadd --gid 1000 sandbox \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash sandbox \
    && mkdir -p /workspace/current /workspace/repos \
    && chown -R sandbox:sandbox /workspace /home/sandbox

COPY --chmod=0755 container/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN install -d -m 0755 /usr/local/share/wisp
COPY --chmod=0644 container/agent-status.js /usr/local/share/wisp/agent-status.js
COPY --chmod=0644 container/opencode.json /usr/local/share/wisp/opencode.json
# Ensure COPY cannot leave the plugin directory inaccessible to the runtime UID.
RUN chmod 0755 /usr/local/share/wisp

ENV HOME=/home/sandbox \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0=safe.directory \
    GIT_CONFIG_VALUE_0=* \
    PATH=/usr/local/go/bin:${PATH}

USER sandbox
RUN test -r /usr/local/share/wisp/agent-status.js
RUN test -r /usr/local/share/wisp/opencode.json
WORKDIR /workspace/current
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
