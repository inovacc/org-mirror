package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var jsonOutput bool

// Version information embedded at build time
var (
	// Version is the application version (from git tag or VERSION env)
	Version = "dev"

	// GitHash is the git commit hash
	GitHash = "none"

	// BuildTime is when the binary was built
	BuildTime = "unknown"
)

// VersionInfo contains all version metadata.
type VersionInfo struct {
	Version   string `json:"version"`
	GitHash   string `json:"git_hash"`
	BuildTime string `json:"build_time"`
}

// versionCmd represents the version command.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long: `Display version information including:
	- Application version
	- Git commit hash
	- Build time`,
	Run: runVersion,
}

func init() {
	rootCmd.AddCommand(versionCmd)
	versionCmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output version info as JSON")
}

func runVersion(cmd *cobra.Command, args []string) {
	if jsonOutput {
		fmt.Println(GetVersionJSON())
	} else {
		printVersion()
	}
}

// GetVersionInfo returns the version information.
func GetVersionInfo() *VersionInfo {
	return &VersionInfo{
		Version:   Version,
		GitHash:   GitHash,
		BuildTime: BuildTime,
	}
}

// GetVersionJSON returns the version information as a JSON string.
func GetVersionJSON() string {
	data, err := json.MarshalIndent(GetVersionInfo(), "", "  ")
	if err != nil {
		return "{}"
	}

	return string(data)
}

func printVersion() {
	fmt.Printf("Version:    %s\n", Version)
	fmt.Printf("Git Hash:   %s\n", GitHash)
	fmt.Printf("Build Time: %s\n", BuildTime)

}
