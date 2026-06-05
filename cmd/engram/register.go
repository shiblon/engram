package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current project in the global manifest",
	Long: `Register adds the current project to the global manifest so it is included
in future 'engram save' archives.

Projects are registered automatically when their engram database is first
created. Use this command for projects whose database already existed before
v0.6.0, or to re-register a project whose identity has changed.`,
	RunE: runRegister,
}

func runRegister(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	gdb, err := engram.OpenGlobalDB(ctx)
	if err != nil {
		return fmt.Errorf("register: open global db: %w", err)
	}
	defer gdb.Close()

	if err := engram.RegisterProject(ctx, gdb, root); err != nil {
		return err
	}

	identity := engram.ProjectIdentity(root)
	fmt.Fprintf(os.Stderr, "registered: %s (%s)\n", root, identity)
	return nil
}

func init() {
	rootCmd.AddCommand(registerCmd)
}
