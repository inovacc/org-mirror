package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newLimitCommand())
}

func newLimitCommand() *cobra.Command {
	var asJSON bool

	command := &cobra.Command{
		Use:   "limit [organization]",
		Short: "Show the authenticated account's GitHub rate-limit budget",
		Long: `Show how much GitHub API budget the authenticated account has left.

Naming an organization adds a line saying whether the remaining core budget
covers discovering that organization's repositories.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			source, _, _, _, err := githubapi.NewAuthenticatedSource("github.com", githubapi.ClientOptions{})
			if err != nil {
				return err
			}
			limits, err := source.RateLimits(command.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return renderLimitsJSON(command.OutOrStdout(), limits)
			}
			if err := renderLimits(command.OutOrStdout(), limits, time.Now()); err != nil {
				return err
			}
			if len(args) == 0 {
				return nil
			}
			count, err := source.RepositoryCount(command.Context(), args[0])
			if err != nil {
				return err
			}
			if verdict := budgetVerdict(limits, count); verdict != "" {
				fmt.Fprintf(command.OutOrStdout(), "\n%s has %d repositories. %s\n", args[0], count, verdict)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "print the rate-limit payload as JSON")
	return command
}

func renderLimits(out io.Writer, limits githubapi.RateLimits, now time.Time) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RESOURCE\tLIMIT\tUSED\tREMAINING\tRESET (UTC)\tIN")
	for _, resource := range limits.Ordered() {
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%s\t%s\n",
			resource.Name, resource.Limit, resource.Used, resource.Remaining,
			resource.ResetAt().Format(time.RFC3339), countdown(resource.ResetAt(), now))
	}
	return writer.Flush()
}

func renderLimitsJSON(out io.Writer, limits githubapi.RateLimits) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(limits)
}

// countdown never renders a negative duration, because a reset in the past
// simply means the budget is already available.
func countdown(reset, now time.Time) string {
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return "now"
	}
	return remaining.Round(time.Second).String()
}

// requestsPerPage matches the page size the repository listing uses.
const requestsPerPage = 100

// budgetVerdict says whether the core budget covers discovering an
// organization of the given size. It is empty when core is not reported.
func budgetVerdict(limits githubapi.RateLimits, repositories int) string {
	core, ok := limits.Resources["core"]
	if !ok {
		return ""
	}
	needed := repositories / requestsPerPage
	if repositories%requestsPerPage != 0 || needed == 0 {
		needed++
	}
	if core.Remaining >= needed {
		return fmt.Sprintf("Discovery needs about %d requests and %d remain, which is enough.", needed, core.Remaining)
	}
	return fmt.Sprintf("Discovery needs about %d requests but only %d remain. The budget resets at %s.",
		needed, core.Remaining, core.ResetAt().Format(time.RFC3339))
}
