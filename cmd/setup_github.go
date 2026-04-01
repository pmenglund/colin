package cmd

import (
	"os"
	"strings"

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

	cmd.Printf("%s repository: %s/%s\n", result.BackendDisplayName, result.RepositoryOwner, result.RepositoryName)
	cmd.Printf("Repository source: %s\n", result.RepositorySource)
	cmd.Printf("Repository URL: %s\n", result.RepositoryURL)
	cmd.Println()
	cmd.Println(result.Instructions)
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

	cmd.Printf("%s repository: %s/%s\n", result.BackendDisplayName, result.RepositoryOwner, result.RepositoryName)
	cmd.Printf("Repository source: %s\n", result.RepositorySource)
	cmd.Printf("Repository URL: %s\n", result.RepositoryURL)
	cmd.Println()
	cmd.Println(strings.ReplaceAll(result.Instructions, "colin setup repo", "colin setup github"))
	return 0
}
