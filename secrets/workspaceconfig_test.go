package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSecretRef(t *testing.T) {
	tests := []struct {
		raw     string
		name    string
		envName string
		wantErr bool
	}{
		{"db-password", "db-password", "DB_PASSWORD", false},
		{"myapp/db-password", "myapp/db-password", "MYAPP_DB_PASSWORD", false},
		{"db-password?as=PGPASSWORD", "db-password", "PGPASSWORD", false},
		{"myapp/sentry-dsn?as=SENTRY_DSN", "myapp/sentry-dsn", "SENTRY_DSN", false},
		{"name?as=lower_ok", "name", "lower_ok", false},
		// invalid
		{"", "", "", true},
		{"name?as=", "", "", true},
		{"name?as=BAD=NAME", "", "", true},
		{"name?as=HAS SPACE", "", "", true},
		{"name?as=HAS\nNEWLINE", "", "", true},
		{"name?as=HAS\tTAB", "", "", true},
		{"name?as=1STARTS_DIGIT", "", "", true},
	}
	for _, tt := range tests {
		name, envName, err := ParseSecretRef(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseSecretRef(%q): expected error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSecretRef(%q): unexpected error: %v", tt.raw, err)
			continue
		}
		if name != tt.name || envName != tt.envName {
			t.Errorf("ParseSecretRef(%q) = (%q, %q), want (%q, %q)", tt.raw, name, envName, tt.name, tt.envName)
		}
	}
}

func TestLoadWorkspaceSecrets_Missing(t *testing.T) {
	ws, err := LoadWorkspaceSecrets(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Secrets) != 0 {
		t.Fatalf("expected empty, got %v", ws.Secrets)
	}
}

func TestLoadWorkspaceSecrets(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".chameleon"), 0700)
	os.WriteFile(filepath.Join(dir, ".chameleon", "secrets.yml"), []byte(`
secrets:
  - db-password
  - "myapp/token?as=API_TOKEN"
`), 0600)

	ws, err := LoadWorkspaceSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %v", ws.Secrets)
	}
}

func TestResolve(t *testing.T) {
	b := newTestBackend(t)
	b.Set("db-password", []byte("hunter2"))
	b.Set("myapp/token", []byte("tok123"))

	env, err := Resolve(b, []string{
		"db-password",
		"myapp/token?as=API_TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DB_PASSWORD=hunter2", "API_TOKEN=tok123"}
	for i, v := range want {
		if env[i] != v {
			t.Errorf("env[%d] = %q, want %q", i, env[i], v)
		}
	}
}

func TestResolve_MissingSecret(t *testing.T) {
	b := newTestBackend(t)
	_, err := Resolve(b, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestResolve_DuplicateEnvName(t *testing.T) {
	b := newTestBackend(t)
	b.Set("a", []byte("1"))
	b.Set("b", []byte("2"))
	_, err := Resolve(b, []string{"a?as=SAME", "b?as=SAME"})
	if err == nil {
		t.Fatal("expected error for duplicate env var name")
	}
}
