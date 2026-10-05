# Azure authentication

The sandbox includes Azure CLI (`az`). To log in automatically with a dedicated
service principal, export all four variables on the host before running Wisp:

```sh
export WISP_AZURE_CLIENT_ID="<application-client-id>"
export WISP_AZURE_TENANT_ID="<tenant-id>"
export WISP_AZURE_SUBSCRIPTION_ID="<subscription-id>"
# Populate WISP_AZURE_CLIENT_SECRET from your secret manager, or prompt without
# putting the value in shell history:
read -r -s -p 'Azure client secret: ' WISP_AZURE_CLIENT_SECRET
printf '\n'
export WISP_AZURE_CLIENT_SECRET
wisp
```

With all four values empty or absent, Azure login is skipped. Partial
configuration fails planning before Docker operations. With all four values
present, the entrypoint runs `az login --service-principal` and selects the
subscription before starting the agent. Either failure stops startup; Azure
command output is suppressed to avoid exposing credentials in errors.

These are runtime inputs, not image build arguments or Wisp config values. They
are forwarded explicitly under their `WISP_` names, not mapped to standard
`AZURE_*` SDK variables. Existing AWS configuration is independent.

## Security and lifetime

Use a dedicated service principal with the minimum necessary permissions. Its
secret is available to the sandbox and to Docker inspection. The naming prefix
is not a security boundary. Do not store credentials in committed files or print
environment dumps. Azure CLI receives the secret as a login argument, so it can
also be visible to process inspection during login.

Wisp does not mount host Azure credentials. The entrypoint sets
`AZURE_CONFIG_DIR` to `/home/sandbox/.azure`, on the sandbox's ephemeral home
filesystem. Tokens remain available to `az` for that sandbox's lifetime and are
not persisted to the host. Wisp does not provide an Azure credential broker or
refresh the client secret automatically. `wisp exec` enters the already-running
sandbox; it does not repeat login.

After updating runtime assets, rebuild the Wisp binary and run `wisp --rebuild`
to refresh existing image tags.
