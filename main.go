package main

import (
	"context"
	"os"

	"github.com/JayWanX/memory/cmd"
)

func main() {
	exitCode := cmd.Execute(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
