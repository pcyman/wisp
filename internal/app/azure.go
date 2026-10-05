package app

import "fmt"

var azureEnvironmentKeys = []string{
	"WISP_AZURE_CLIENT_ID",
	"WISP_AZURE_TENANT_ID",
	"WISP_AZURE_SUBSCRIPTION_ID",
	"WISP_AZURE_CLIENT_SECRET",
}

func planAzureEnvironment(inherited []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, key := range azureEnvironmentKeys {
		if value := environmentValue(inherited, key); value != "" {
			values[key] = value
		}
	}
	if len(values) != 0 && len(values) != len(azureEnvironmentKeys) {
		return nil, fmt.Errorf("Azure login requires all four WISP_AZURE_CLIENT_ID, WISP_AZURE_TENANT_ID, WISP_AZURE_SUBSCRIPTION_ID, and WISP_AZURE_CLIENT_SECRET variables")
	}
	return values, nil
}
