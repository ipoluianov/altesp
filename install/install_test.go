package install

import "testing"

func TestVersionNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v0.0.18", "0.0.17", true},
		{"v0.0.17", "0.0.17", false},
		{"v0.0.17", "0.0.18", false},
		{"v0.1.0", "0.0.99", true},
		{"v0.0.17-3-g1dfcbdf", "0.0.17", true},
		{"v0.0.17", "0.0.17-3-g1dfcbdf", false},
		{"v0.0.17-3-g1dfcbdf-dirty", "0.0.17-3-g1dfcbdf", false},
		{"v1.2", "1.2.0", false},
		{"dev", "0.0.17", true},
		{"v0.0.17", "dev", true},
		{"v0.0.17", "", true},
	}
	for _, tt := range tests {
		if got := versionNewer(tt.a, tt.b); got != tt.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
