package version

import "testing"

func TestString(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	for _, tt := range []struct {
		version string
		want    string
	}{{"", "dev"}, {"dev", "dev"}, {"1.2.3", "1.2.3"}} {
		Version = tt.version
		if got := String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
