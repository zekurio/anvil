package language

import "testing"

func TestNormalizeTreatsUndefinedAsUnknown(t *testing.T) {
	for _, value := range []string{"und", "unknown", "undefined"} {
		if got := Normalize(value); got != "" {
			t.Fatalf("Normalize(%q) = %q, want empty unknown language", value, got)
		}
	}
}
