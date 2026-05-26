// Command box is the entrypoint for the `box` CLI. All logic lives in
// box/cli; this file deliberately stays thin so tests can drive Run directly.
package main

import (
	"os"

	"github.com/windborneos/box-model/box/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdin, os.Stdout, os.Stderr, os.Getenv, nil))
}
