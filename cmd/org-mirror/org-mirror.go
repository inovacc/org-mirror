package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "org-mirror",
	Short: "Mirror GitHub organization repositories as working copies",
	Long: `Mirror GitHub organization repositories as working copies

This is a CLI application built with Cobra.`,
}

// newExecutionContext builds the context Execute runs the root command
// against: one an interrupt or SIGTERM cancels, so a mirror run - holding a
// live git child process and an open database run row - gets the chance to
// record its final status instead of being killed outright. A second signal
// still kills immediately, because NotifyContext restores the default
// disposition after the first one - the right behaviour when a git child is
// wedged and the operator wants out now. Extracted from Execute so the
// wiring itself is testable without invoking Execute, which parses the real
// command line.
func newExecutionContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	ctx, stop := newExecutionContext()
	defer stop()
	cobra.CheckErr(rootCmd.ExecuteContext(ctx))
}

func main() {
	Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")
}
