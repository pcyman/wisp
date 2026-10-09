package app

import "strings"

// planEnvPassthrough distinguishes an unset variable from an explicitly empty
// value. Host environment values are captured once, without consulting os.Getenv.
func planEnvPassthrough(names, inherited []string) map[string]string {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	values := make(map[string]string, len(names))
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if ok && selected[name] {
			values[name] = value
		}
	}
	return values
}

// Passthrough uses literal sandbox-only override values. Remove the selected
// values from run-scoped host subprocess environments too.
func withoutPassthrough(environment []string, passthrough map[string]string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if _, selected := passthrough[name]; !selected {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
