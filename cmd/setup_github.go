package cmd

import (
	"os"
	"strings"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupRepo(cmd *cobra.Command, workflowPath string) int {
	workingDir, err := os.Getwd()
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}

	result, err := service.LoadRepoTokenSetup(workflowPath, workingDir)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	renderRepoTokenSetup(cmd, result, result.Instructions)
	return 0
}

func runSetupGitHub(cmd *cobra.Command, workflowPath string) int {
	workingDir, err := os.Getwd()
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}

	result, err := service.LoadRepoTokenSetup(workflowPath, workingDir)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}
	if !strings.EqualFold(result.Backend, "github") {
		cmd.PrintErrln("workflow backend is not github; run `colin setup repo` instead")
		return 1
	}

	renderRepoTokenSetup(cmd, result, strings.ReplaceAll(result.Instructions, "colin setup repo", "colin setup github token"))
	return 0
}

func renderRepoTokenSetup(cmd *cobra.Command, result service.RepoTokenSetupResult, instructions string) {
	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	renderer.Item(result.BackendDisplayName+" repository", result.RepositoryOwner+"/"+result.RepositoryName)
	renderer.Item("Repository source", result.RepositorySource)
	renderer.Item("Repository URL", result.RepositoryURL)
	if result.RecommendedEnvVar != "" {
		renderer.Item("Recommended env var", result.RecommendedEnvVar)
	}

	lines := linesWithoutHeading(instructions)
	if len(lines) == 0 {
		return
	}

	renderer.Section("Next steps")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			renderer.Line("")
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			renderer.Status(clioutput.StatusAction, "", strings.TrimPrefix(trimmed, "- "))
			continue
		}
		renderer.Line(trimmed)
	}
}
