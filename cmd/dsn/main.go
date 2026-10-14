// Command dsn runs the Dynasty Sports Network: save watcher, ingest, and site.
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: dsn <command> [flags]

commands:
  serve     run the website and the save watcher (default)
  ingest    ingest one save file (copies it first) and exit
  reingest  re-ingest every archived save in timeline order
  extract   print a summary of what a save contains (debug)`)
}

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "ingest":
		err = runIngest(args)
	case "reingest":
		err = runReingest(args)
	case "extract":
		err = runExtract(args)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dsn:", err)
		os.Exit(1)
	}
}
