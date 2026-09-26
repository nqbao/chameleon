package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestBackend(t *testing.T) *LocalBackend {
	t.Helper()
	dir := t.TempDir()
	return &LocalBackend{
		Dir:     filepath.Join(dir, "secrets"),
		KeyFile: filepath.Join(dir, "key"),
	}
}

func TestSetGet(t *testing.T) {
	b := newTestBackend(t)
	if err := b.Set("db-password", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get("db-password")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q, want %q", got, "hunter2")
	}
}

func TestSetGetNestedPath(t *testing.T) {
	b := newTestBackend(t)
	if err := b.Set("myapp/db-password", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get("myapp/db-password")
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret" {
		t.Fatalf("got %q, want %q", got, "secret")
	}
}

func TestSetOverwrite(t *testing.T) {
	b := newTestBackend(t)
	b.Set("key", []byte("first"))
	b.Set("key", []byte("second"))
	got, _ := b.Get("key")
	if got != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

func TestGetNotFound(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestKeyCreatedOnFirstSet(t *testing.T) {
	b := newTestBackend(t)
	if _, err := os.Stat(b.KeyFile); !os.IsNotExist(err) {
		t.Fatal("key file should not exist before first Set")
	}
	b.Set("x", []byte("v"))
	if _, err := os.Stat(b.KeyFile); err != nil {
		t.Fatal("key file should exist after Set:", err)
	}
}

func TestKeyPermissions(t *testing.T) {
	b := newTestBackend(t)
	b.Set("x", []byte("v"))
	info, err := os.Stat(b.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key file permissions %04o, want 0600", perm)
	}
}

func TestKeyReused(t *testing.T) {
	b := newTestBackend(t)
	b.Set("a", []byte("alpha"))
	b.Set("b", []byte("beta"))

	a, _ := b.Get("a")
	bb, _ := b.Get("b")
	if a != "alpha" || bb != "beta" {
		t.Fatalf("got a=%q b=%q", a, bb)
	}
}

func TestList(t *testing.T) {
	b := newTestBackend(t)

	names, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty list, got %v", names)
	}

	b.Set("alpha", []byte("1"))
	b.Set("myapp/beta", []byte("2"))
	b.Set("myapp/gamma", []byte("3"))

	names, err = b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 secrets, got %v", names)
	}
	want := map[string]bool{"alpha": true, "myapp/beta": true, "myapp/gamma": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected name %q", n)
		}
	}
}

func TestDelete(t *testing.T) {
	b := newTestBackend(t)
	b.Set("x", []byte("v"))
	if err := b.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get("x"); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDeleteNotFound(t *testing.T) {
	b := newTestBackend(t)
	if err := b.Delete("nonexistent"); err == nil {
		t.Fatal("expected error deleting nonexistent secret")
	}
}

func TestDeletePrunesEmptyDirs(t *testing.T) {
	b := newTestBackend(t)
	b.Set("myapp/db-password", []byte("v"))
	b.Delete("myapp/db-password")

	if _, err := os.Stat(filepath.Join(b.Dir, "myapp")); !os.IsNotExist(err) {
		t.Fatal("empty parent dir should be removed after delete")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	b := newTestBackend(t)
	bad := []string{"../key", "../../etc/passwd", "a/../b", "..", "a/./b", "a//b", ""}
	for _, name := range bad {
		if err := b.Set(name, []byte("v")); err == nil {
			t.Errorf("Set(%q): expected error", name)
		}
		if _, err := b.Get(name); err == nil {
			t.Errorf("Get(%q): expected error", name)
		}
		if err := b.Delete(name); err == nil {
			t.Errorf("Delete(%q): expected error", name)
		}
	}
}

func TestDeleteKeepsNonEmptyDirs(t *testing.T) {
	b := newTestBackend(t)
	b.Set("myapp/alpha", []byte("1"))
	b.Set("myapp/beta", []byte("2"))
	b.Delete("myapp/alpha")

	if _, err := os.Stat(filepath.Join(b.Dir, "myapp")); err != nil {
		t.Fatal("non-empty parent dir should survive delete:", err)
	}
	got, err := b.Get("myapp/beta")
	if err != nil || got != "2" {
		t.Fatalf("sibling secret should be unaffected: got=%q err=%v", got, err)
	}
}
