package version

// These values may be replaced with -ldflags at release build time.
var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
)

// String returns the stable version shown by the version command.
func String() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
