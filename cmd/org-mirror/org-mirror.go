package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "org-mirror",
	Short: "Mirror GitHub organization repositories as working copies",
	Long: `Mirror GitHub organization repositories as working copies

This is a CLI application built with Cobra.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

func main() {
	Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")
}
