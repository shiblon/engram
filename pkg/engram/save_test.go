package engram

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// readArchive decompresses and lists all entry names in a tgz blob.
func readArchive(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	entries := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		entries[hdr.Name] = body
	}
	return entries
}

// setupProjectDB creates a project DB at root/.engram/mem.db with at least one
// memory so the directory / DB exist on disk.
func setupProjectDB(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	db, err := OpenProjectDB(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "v"})
	db.Close()
}

func TestSaveArchiveLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	// Create a project and register it.
	projRoot := filepath.Join(home, "code", "myproject")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitRemote(t, projRoot, [][2]string{{"origin", "git@github.com:me/myproject.git"}})
	setupProjectDB(t, projRoot)

	// Register in global db.
	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterProject(ctx, gdb, projRoot); err != nil {
		t.Fatal(err)
	}
	gdb.Close()

	var buf bytes.Buffer
	result, err := Save(ctx, &buf, SaveOptions{EngramVersion: "test"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if result.ProjectCount != 1 {
		t.Errorf("ProjectCount = %d, want 1", result.ProjectCount)
	}
	if result.PrunedCount != 0 {
		t.Errorf("PrunedCount = %d, want 0", result.PrunedCount)
	}

	entries := readArchive(t, buf.Bytes())

	for _, required := range []string{
		"meta.json",
		"global/mem.db",
		"projects/0/mem.db",
		"projects/0/project.json",
	} {
		if _, ok := entries[required]; !ok {
			t.Errorf("archive missing %q", required)
		}
	}

	// meta.json should decode cleanly and reference our project.
	var meta SaveMeta
	if err := json.Unmarshal(entries["meta.json"], &meta); err != nil {
		t.Fatalf("meta.json decode: %v", err)
	}
	if meta.EngramVersion != "test" {
		t.Errorf("EngramVersion = %q", meta.EngramVersion)
	}
	if len(meta.Projects) != 1 || meta.Projects[0].Identity != "git@github.com:me/myproject.git" {
		t.Errorf("meta.Projects = %v", meta.Projects)
	}

	// project.json sidecar must match.
	var sp SaveProject
	if err := json.Unmarshal(entries["projects/0/project.json"], &sp); err != nil {
		t.Fatalf("project.json decode: %v", err)
	}
	if sp.Identity != "git@github.com:me/myproject.git" {
		t.Errorf("sidecar identity = %q", sp.Identity)
	}
}

func TestSaveContextExcludedByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	projRoot := filepath.Join(home, "code", "ctxproj")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	setupProjectDB(t, projRoot)

	// Create a context/long.md to trigger the warning.
	ctxDir := filepath.Join(projRoot, "context")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "long.md"), []byte("# memories\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gdb, _ := OpenGlobalDB(ctx)
	_ = RegisterProject(ctx, gdb, projRoot)
	gdb.Close()

	var buf bytes.Buffer
	result, err := Save(ctx, &buf, SaveOptions{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries := readArchive(t, buf.Bytes())
	for name := range entries {
		if len(name) > 8 && name[:8] == "projects" {
			if filepath.Base(name) == "long.md" {
				t.Errorf("context/long.md should not appear in archive without --include-context, got %q", name)
			}
		}
	}

	if len(result.ContextWarnings) == 0 {
		t.Error("expected ContextWarnings when context/ exists and IncludeContext=false")
	}
}

func TestSaveContextIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	projRoot := filepath.Join(home, "code", "ctxincl")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	setupProjectDB(t, projRoot)

	ctxDir := filepath.Join(projRoot, "context")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "long.md"), []byte("# memories\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gdb, _ := OpenGlobalDB(ctx)
	_ = RegisterProject(ctx, gdb, projRoot)
	gdb.Close()

	var buf bytes.Buffer
	result, err := Save(ctx, &buf, SaveOptions{IncludeContext: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(result.ContextWarnings) != 0 {
		t.Errorf("unexpected warnings with IncludeContext=true: %v", result.ContextWarnings)
	}

	entries := readArchive(t, buf.Bytes())
	found := false
	for name := range entries {
		if filepath.Base(name) == "long.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("long.md not found in archive with IncludeContext=true")
	}
}

func TestSavePrunesDead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	// Register a project that no longer exists on disk.
	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterProject(ctx, gdb, filepath.Join(home, "ghost")); err != nil {
		t.Fatal(err)
	}
	gdb.Close()

	var buf bytes.Buffer
	result, err := Save(ctx, &buf, SaveOptions{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if result.PrunedCount != 1 {
		t.Errorf("PrunedCount = %d, want 1", result.PrunedCount)
	}
	if result.ProjectCount != 0 {
		t.Errorf("ProjectCount = %d, want 0 (ghost was pruned)", result.ProjectCount)
	}
}
