package main

import (
	"os"

	"github.com/vshulcz/deja-vu/internal/instructions"
)

func init() {
	commands["instructions"] = func(_ string, args []string) error {
		return instructions.Run(args, os.Stdin, os.Stdout)
	}
	// Experimental, opt-in, documented in its own help and guide until full
	// repository and real-harness delivery validation has been completed.
	helpHidden["instructions"] = true
	worksWithNoHome["instructions"] = true // the independent store validates its path
}
