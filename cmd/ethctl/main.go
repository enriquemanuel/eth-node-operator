package main

import (
	"fmt"
	"os"

	"github.com/enriquemanuel/eth-node-operator/cli/commands"
)

func main() {
	if err := commands.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
