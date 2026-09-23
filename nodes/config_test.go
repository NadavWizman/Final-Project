package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("# comment\nTD_A=from-file\nexport TD_B=\"quoted\"\nTD_C=from-file\n"), 0600)
	t.Setenv("TD_C", "from-env")
	os.Unsetenv("TD_A")
	os.Unsetenv("TD_B")
	t.Cleanup(func() { os.Unsetenv("TD_A"); os.Unsetenv("TD_B") })

	loadDotEnv(path)

	for key, want := range map[string]string{"TD_A": "from-file", "TD_B": "quoted", "TD_C": "from-env"} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
