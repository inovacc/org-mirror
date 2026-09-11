package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/spf13/cobra"
)

const defaultMirrorRoot = `C:\Users\dyamm\Downloads\mirror\orgs`

func init() {
	rootCmd.AddCommand(newSyncCommand())
}

func newSyncCommand() *cobra.Command {
	var root string
	var dryRun bool

	command := &cobra.Command{
		Use:   "sync <organization>",
		Short: "Mirror an organization as local working copies",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			organization := args[0]
			metadata, err := mirror.NewService(mirror.OSRunner{}).Mirror(context.Background(), organization, root, dryRun)
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
	return command
}
