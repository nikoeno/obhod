package version

// Set via: -ldflags "-X <module>/internal/lib/version.Version=v0.1.0 -X <module>/internal/lib/version.Commit=abc123"
var (
	// Version is the release version; "dev" for local builds.
	Version = "dev"
	// Commit is the source revision, empty when unknown.
	Commit = ""
)

// String returns "<binary> <Version>" plus " (<Commit>)" if Commit is set.
func String(binary string) string {
	s := binary + " " + Version
	if Commit != "" {
		s += " (" + Commit + ")"
	}
	return s
}
