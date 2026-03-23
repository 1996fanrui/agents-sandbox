package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintf(os.Stderr, "agbox: skeleton initialized, subcommands not implemented yet (args=%d)\n", len(os.Args)-1)
	os.Exit(0)
}
