package main

import (
	"os"

	"github.com/thanhhoanace/ezharness/v6/internal/evidence"
)

func main() {
	os.Exit(evidence.Run(os.Args[1:], os.Stdout, os.Stderr))
}
