package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fchimpan/release-proxy/cmd"
)

func main() {
	if err := cmd.Run(context.Background(), os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
