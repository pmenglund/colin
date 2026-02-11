package main

import (
	"fmt"
	"os"

	"github.com/pmenglund/colin/cmd"
)

func main() {
	if err := cmd.NewRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
