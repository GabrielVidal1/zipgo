package zipconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".zipgo.json"), `{"target":"u@h:/base","targets":{"raspy2":"u@h:/base"}}`)
	nested := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	path, cfg, found, err := FindRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected to find .zipgo.json in an ancestor")
	}
	if filepath.Dir(path) != root {
		t.Errorf("found at %q, want under %q", path, root)
	}
	if cfg.Target != "u@h:/base" {
		t.Errorf("Target = %q", cfg.Target)
	}
	if cfg.Targets["raspy2"] != "u@h:/base" {
		t.Errorf("Targets = %v", cfg.Targets)
	}
}

func TestFindRootMissing(t *testing.T) {
	_, _, found, err := FindRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false when no .zipgo.json exists")
	}
}

func TestFindProject(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "package.json"),
		`{"name":"x","zipgo":{"deploy":{"a.gabvdl.xyz":"dist","b.dev.gabvdl.xyz":"dist/b"},"target":"raspy2"}}`)
	sub := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, cfg, found, err := FindProject(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected to find package.json with zipgo key")
	}
	if got != dir {
		t.Errorf("dir = %q, want %q", got, dir)
	}
	if cfg.Deploy["a.gabvdl.xyz"] != "dist" || cfg.Deploy["b.dev.gabvdl.xyz"] != "dist/b" {
		t.Errorf("Deploy = %v", cfg.Deploy)
	}
	if cfg.Target != "raspy2" {
		t.Errorf("Target = %q", cfg.Target)
	}
}

func TestFindProjectSkipsPackageWithoutZipgo(t *testing.T) {
	dir := t.TempDir()
	// Ancestor has the zipgo config; nested package.json has none and must be
	// skipped so the search keeps ascending.
	write(t, filepath.Join(dir, "package.json"), `{"zipgo":{"deploy":{"a.gabvdl.xyz":"dist"}}}`)
	nested := filepath.Join(dir, "packages", "inner")
	write(t, filepath.Join(nested, "package.json"), `{"name":"inner"}`)

	got, cfg, found, err := FindProject(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != dir {
		t.Fatalf("found=%v dir=%q, want found at %q", found, got, dir)
	}
	if cfg.Deploy["a.gabvdl.xyz"] != "dist" {
		t.Errorf("Deploy = %v", cfg.Deploy)
	}
}

func TestFindProjectMissing(t *testing.T) {
	_, _, found, err := FindProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false when no package.json with zipgo exists")
	}
}
