package cmd

import (
	"os"

	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupGitHub(cmd *cobra.Command, workflowPath string) int {
	workingDir, err := os.Getwd()
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}

	result, err := service.LoadGitHubTokenSetup(workflowPath, workingDir)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	cmd.Printf("GitHub repository: %s/%s\n", result.RepositoryOwner, result.RepositoryName)
	cmd.Printf("Repository source: %s\n", result.RepositorySource)
	cmd.Printf("Repository URL: %s\n", result.RepositoryURL)
	cmd.Println()
	cmd.Println(githubauth.RenderInstructions(githubauth.SetupDetails{
		Repository: githubauth.Repository{
			Owner: result.RepositoryOwner,
			Name:  result.RepositoryName,
			URL:   result.RepositoryURL,
		},
		FineGrainedTokenURL: result.FineGrainedTokenURL,
	}, "colin setup github"))
	return 0
}
