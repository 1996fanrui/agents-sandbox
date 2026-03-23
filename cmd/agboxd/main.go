package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintf(os.Stderr, "agboxd: skeleton initialized, daemon not implemented yet (args=%d)\n", len(os.Args)-1)
	os.Exit(0)
}
