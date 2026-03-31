package main

import (
	"os"

	colincmd "github.com/pmenglund/colin/cmd"
)

func main() {
	os.Exit(colincmd.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
