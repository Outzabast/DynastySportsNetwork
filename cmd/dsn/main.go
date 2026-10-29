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
  extract   print a summary of what a save contains (debug)
  backfill-team-stats  refill empty team season totals from the archive
  repair-fcs-lines     move FCS players' game lines off the FBS team (from the archive)
  prune-archive        delete superseded saves (older saves of the same week) from the archive
  backfill-rosters     record past seasons' full rosters from the archive
  repair-season-conferences  fix seasons recorded with next season's conferences
  backfill-draft-awards  record past NFL draft picks and weekly award seasons from the archive`)
}

func main() {
	loadDotEnv(".env")
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
	case "backfill-team-stats":
		err = runBackfillTeamStats(args)
	case "repair-fcs-lines":
		err = runRepairFCSLines(args)
	case "prune-archive":
		err = runPruneArchive(args)
	case "backfill-rosters":
		err = runBackfillRosters(args)
	case "repair-season-conferences":
		err = runRepairSeasonConferences(args)
	case "backfill-draft-awards":
		err = runBackfillDraftAwards(args)
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
