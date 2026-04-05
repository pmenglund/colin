package repoops

import "github.com/pmenglund/colin/internal/repohost"

//go:generate go tool counterfeiter -o ./fakes/fake_repo_host_client.go . RepoHostClient

type RepoHostClient interface {
	repohost.Client
}
