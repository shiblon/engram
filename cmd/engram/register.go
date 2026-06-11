package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var registerScanDir string
var registerList bool

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current project (or scan a directory tree) in the global manifest",
	Long: `Register adds projects to the global manifest so they are included in future
'engram save' archives.

Without flags, registers the project rooted at the current directory (or --cwd).

  --list          Print all projects currently in the manifest.
  --scan <dir>    Walk <dir>, find every .engram/mem.db, and register them all.

Projects are registered automatically when their engram database is first
created. Use this command for projects that predate v0.6.0 or whose identity
has changed.`,
	RunE: runRegister,
}

func runRegister(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	gdb, err := engram.OpenGlobalDB(ctx)
	if err != nil {
		return fmt.Errorf("register: open global db: %w", err)
	}
	defer gdb.Close()

	if registerList {
		return runRegisterList(ctx, gdb)
	}

	if registerScanDir != "" {
		return runRegisterScan(ctx, gdb, registerScanDir)
	}

	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if err := engram.RegisterProject(ctx, gdb, root); err != nil {
		return err
	}
	identity := engram.ProjectIdentity(root)
	fmt.Fprintf(os.Stderr, "registered: %s (%s)\n", engram.ProjectStorageRoot(root), identity)
	noteSiblingCopies(ctx, gdb, identity)
	return nil
}

// noteSiblingCopies prints an informational note when the just-registered repo
// shares its identity with other live working copies, so the user knows a
// parallel copy (a separate clone) exists rather than assuming this is the only
// checkout. Linked git worktrees share the main checkout's manifest row.
// Informational only -- registering a second clone is a supported, non-error
// action, not something to gate behind a --force flag.
func noteSiblingCopies(ctx context.Context, gdb *sql.DB, identity string) {
	rows, err := gdb.QueryContext(ctx,
		`SELECT path FROM projects WHERE identity = ? AND status = 'live' ORDER BY last_seen DESC`, identity)
	if err != nil {
		return
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	if len(paths) > 1 {
		fmt.Fprintf(os.Stderr, "note: %q now has %d registered working copies:\n", identity, len(paths))
		for _, p := range paths {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
	}
}

// runRegisterScan walks scanRoot, finds every directory containing
// .engram/mem.db, and registers each as a project. It skips the global
// $HOME/.engram directory and common non-project subtrees (node_modules,
// vendor) to stay fast on large trees.
func runRegisterScan(ctx context.Context, gdb *sql.DB, scanRoot string) error {
	abs, err := filepath.Abs(scanRoot)
	if err != nil {
		return fmt.Errorf("register --scan: resolve path: %w", err)
	}

	home, _ := os.UserHomeDir()
	globalEngram := filepath.Join(home, ".engram")

	var registered, skipped int

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // permission error etc.; keep walking
		}
		if !d.IsDir() {
			return nil
		}

		// Skip the global engram dir — it's not a project root.
		if path == globalEngram {
			return fs.SkipDir
		}

		// Skip dirs that will never contain engram projects.
		switch d.Name() {
		case "node_modules", "vendor", ".git", ".hg", ".svn":
			return fs.SkipDir
		}

		// Found an .engram dir — check for mem.db and register the parent.
		if d.Name() == ".engram" {
			dbPath := filepath.Join(path, "mem.db")
			if _, err := os.Stat(dbPath); err == nil {
				root := filepath.Dir(path)
				if err := engram.RegisterProject(ctx, gdb, root); err != nil {
					fmt.Fprintf(os.Stderr, "warning: register %s: %v\n", root, err)
					skipped++
				} else {
					fmt.Fprintf(os.Stderr, "registered: %s (%s)\n", engram.ProjectStorageRoot(root), engram.ProjectIdentity(root))
					registered++
				}
			}
			return fs.SkipDir // no projects nested inside .engram
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("register --scan: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n%d registered", registered)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, ", %d failed", skipped)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

func runRegisterList(ctx context.Context, gdb *sql.DB) error {
	rows, err := gdb.QueryContext(ctx,
		`SELECT identity, path, status FROM projects ORDER BY status, last_seen DESC`)
	if err != nil {
		return fmt.Errorf("register --list: %w", err)
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var identity, path, status string
		if err := rows.Scan(&identity, &path, &status); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%-8s  %-40s  %s\n", status, path, identity)
		n++
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "no projects registered")
	}
	return rows.Err()
}

func init() {
	registerCmd.Flags().BoolVar(&registerList, "list", false, "list all projects in the manifest")
	registerCmd.Flags().StringVar(&registerScanDir, "scan", "", "scan this directory tree and register all projects found")
	rootCmd.AddCommand(registerCmd)
}
