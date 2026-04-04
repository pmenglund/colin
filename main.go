package main

import (
	"os"

	colincmd "github.com/pmenglund/colin/cmd"
	"github.com/pmenglund/colin/internal/repohost/builtin"
)

func main() {
	builtin.Register()
	os.Exit(colincmd.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
