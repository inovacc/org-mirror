package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/inovacc/org-mirror/internal/githubapi"
	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/inovacc/org-mirror/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const defaultMirrorRoot = `C:\Users\dyamm\Downloads\mirror\orgs`

func init() {
	rootCmd.AddCommand(newSyncCommand())
}

func newSyncCommand() *cobra.Command {
	var root string
	var dryRun bool
	var noTUI bool

	command := &cobra.Command{
		Use:   "sync <organization>",
		Short: "Mirror an organization as local working copies",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			organization := args[0]
			source, token, _, err := githubapi.NewAuthenticatedSource("github.com")
			if err != nil {
				return err
			}
			service := mirror.NewService(mirror.OSRunner{GitHubToken: token}, source)
			var metadata mirror.Metadata
			if shouldUseTUI(noTUI, command.OutOrStdout(), term.IsTerminal) {
				metadata, err = tui.Run(command.Context(), organization, dryRun, func(ctx context.Context, report mirror.ProgressFunc) (mirror.Metadata, error) {
					return service.MirrorWithProgress(ctx, organization, root, dryRun, report)
				})
			} else {
				metadata, err = service.Mirror(command.Context(), organization, root, dryRun)
			}
			if err != nil {
				return err
			}
			for _, result := range metadata.Repositories {
				if result.Message == "" {
					fmt.Fprintf(command.OutOrStdout(), "%s: %s\n", result.Repository.NameWithOwner, result.Outcome)
					continue
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: %s (%s)\n", result.Repository.NameWithOwner, result.Outcome, result.Message)
			}
			if dryRun {
				fmt.Fprintln(command.OutOrStdout(), "dry-run: metadata was not written")
				return nil
			}
			fmt.Fprintf(command.OutOrStdout(), "metadata: %s\n", filepath.Join(root, organization, "metadata.json"))
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", defaultMirrorRoot, "directory that contains organization mirrors")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report actions without changing repositories or metadata")
	command.Flags().BoolVar(&noTUI, "no-tui", false, "disable the interactive progress interface")
	return command
}

func shouldUseTUI(disabled bool, output io.Writer, isTerminal func(int) bool) bool {
	if disabled {
		return false
	}
	file, ok := output.(interface{ Fd() uintptr })
	return ok && isTerminal(int(file.Fd()))
}
