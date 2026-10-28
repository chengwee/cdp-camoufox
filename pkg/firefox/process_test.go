package firefox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPageWorldEvalEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		fallback string
		chunk1   string
		chunk2   string
		enabled  bool
		want     string
	}{
		{"fallback", `{"other":42,"disableWorldIsolation":false}`, "", "", true, `{"other":42,"disableWorldIsolation":true}`},
		{"chunks take precedence", `{"fallback":true}`, `{"chunk":`, `42}`, true, `{"chunk":42,"disableWorldIsolation":true}`},
		{"disabled unchanged", `{"other":42}`, `{"chunk":`, `42}`, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if suffix, ok := strings.CutPrefix(key, "CAMOU_CONFIG_"); ok {
					if _, err := strconv.Atoi(suffix); err == nil {
						t.Setenv(key, "")
					}
				}
			}
			t.Setenv("CAMOU_CONFIG", test.fallback)
			if test.chunk1 != "" {
				t.Setenv("CAMOU_CONFIG_1", test.chunk1)
			}
			if test.chunk2 != "" {
				t.Setenv("CAMOU_CONFIG_2", test.chunk2)
			}
			t.Setenv("FOXBRIDGE_TEST_OTHER", "preserved")
			output := filepath.Join(t.TempDir(), "env")
			binary := filepath.Join(t.TempDir(), "browser")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nenv > \"$FOXBRIDGE_TEST_OUTPUT\"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FOXBRIDGE_TEST_OUTPUT", output)
			process := New()
			if err := process.Start(Config{BinaryPath: binary, PageWorldEval: test.enabled}); err != nil {
				t.Fatal(err)
			}
			if err := process.Wait(); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				if _, exists := env[key]; exists {
					t.Fatalf("duplicate environment key %s", key)
				}
				env[key] = value
			}
			if env["FOXBRIDGE_TEST_OTHER"] != "preserved" {
				t.Fatal("unrelated environment lost")
			}
			if !test.enabled {
				if env["CAMOU_CONFIG"] != test.fallback || env["CAMOU_CONFIG_1"] != test.chunk1 || env["CAMOU_CONFIG_2"] != test.chunk2 {
					t.Fatalf("environment changed: %v", env)
				}
				return
			}
			var got, want map[string]interface{}
			if err := json.Unmarshal([]byte(env["CAMOU_CONFIG_1"]+env["CAMOU_CONFIG_2"]), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for key, value := range want {
				if got[key] != value {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestPageWorldEvalRejectsInvalidConfigAndBiDi(t *testing.T) {
	t.Setenv("CAMOU_CONFIG_1", "{invalid")
	if err := New().Start(Config{BinaryPath: "/unused", PageWorldEval: true}); err == nil || !strings.Contains(err.Error(), "CAMOU_CONFIG") {
		t.Fatalf("expected CAMOU_CONFIG error, got %v", err)
	}
	if err := New().Start(Config{BinaryPath: "/unused", BiDiPort: 9223, PageWorldEval: true}); err == nil || !strings.Contains(err.Error(), "BiDi") {
		t.Fatalf("expected BiDi error, got %v", err)
	}
}
