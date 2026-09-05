package mountpolicy

import "testing"

func TestValidateTarget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		target string
		valid  bool
	}{
		{name: "descendant", target: "/workspace/repos/project", valid: true},
		{name: "root", target: "/workspace/repos"},
		{name: "unclean", target: "/workspace/repos/project/.."},
		{name: "outside", target: "/workspace/current"},
		{name: "relative", target: "workspace/repos/project"},
		{name: "NUL", target: "/workspace/repos/a\x00b"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTarget(test.target)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateTarget(%q) error = %v, valid = %v", test.target, err, test.valid)
			}
		})
	}
}

func TestOverlaps(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		a, b string
		want bool
	}{
		{a: "/workspace/repos/a", b: "/workspace/repos/a", want: true},
		{a: "/workspace/repos/a", b: "/workspace/repos/a/child", want: true},
		{a: "/workspace/repos/a/child", b: "/workspace/repos/a", want: true},
		{a: "/workspace/repos/a", b: "/workspace/repos/ab"},
	} {
		if got := Overlaps(test.a, test.b); got != test.want {
			t.Errorf("Overlaps(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
		}
	}
}
