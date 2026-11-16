package main

import (
	"fmt"
	"os"

	"github.com/Jairanpo/flamingo/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "flamingo:", err)
		os.Exit(1)
	}
}
