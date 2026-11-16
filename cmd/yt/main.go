package main

import (
	"fmt"
	"os"

	"github.com/Jairanpo/yt/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "yt:", err)
		os.Exit(1)
	}
}
