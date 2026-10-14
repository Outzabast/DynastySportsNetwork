package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

func runExtract(args []string) error {
	var c config
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	c.bind(fs)
	out := fs.String("o", "", "write the full snapshot JSON here")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("extract needs one save path (pass a copy)")
	}
	start := time.Now()
	s, err := extract.Extract(fs.Arg(0), c.SchemaDir)
	if err != nil {
		return err
	}
	fmt.Printf("league=%d season=%d (idx %d) %s week %d %s  parsed in %s\n", s.LeagueID, s.Season.Year, s.Season.SeasonIndex, s.Season.Stage, s.Season.Week, s.Season.WeekType, time.Since(start).Round(time.Millisecond))
	fmt.Printf("teams=%d players=%d coaches=%d games=%d recruits=%d\n", len(s.Teams), len(s.Players), len(s.Coaches), len(s.Games), len(s.Recruits))
	count := func(name string, n int) { fmt.Printf("  %-20s %d\n", name, n) }
	cats := map[string]int{}
	for _, l := range s.PlayerGameStats {
		cats["game/"+l.Category]++
	}
	for _, l := range s.PlayerSeasonStats {
		cats["season/"+l.Category]++
	}
	for _, l := range s.PlayerCareerStats {
		cats["career/"+l.Category]++
	}
	keys := make([]string, 0, len(cats))
	for k := range cats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		count(k, cats[k])
	}
	plays, boxes := 0, 0
	for _, g := range s.Games {
		plays += len(g.ScoringPlays)
		if g.HomeBox != nil {
			boxes++
		}
	}
	count("scoring plays", plays)
	count("team box scores", boxes)
	count("team season stats", len(s.TeamSeasonStats))
	count("player awards", len(s.PlayerAwards))
	count("league awards", len(s.LeagueAwards))
	count("record book", len(s.RecordBook))
	if *out != "" {
		b, err := s.JSON()
		if err != nil {
			return err
		}
		return os.WriteFile(*out, b, 0o644)
	}
	return nil
}
