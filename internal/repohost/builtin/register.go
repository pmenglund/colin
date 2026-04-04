package builtin

import (
	"sync"

	"github.com/pmenglund/colin/internal/repohost"
	repogithub "github.com/pmenglund/colin/internal/repohost/github"
)

var once sync.Once

// Register installs the built-in repository-host adapters.
func Register() {
	once.Do(func() {
		repohost.Register(repogithub.Adapter{})
	})
}
