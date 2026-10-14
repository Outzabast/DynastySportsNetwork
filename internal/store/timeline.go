package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

// stageRank orders the season stages we have observed. Unknown stages sort
// with the in-season stage and are flagged for review so the real sequence can
// be learned from data rather than hardcoded.
func stageRank(stage string) (int, bool) {
	switch stage {
	case "PreSeason":
		return 0, true
	case "NFLSeason":
		return 1, true
	case "OffSeason":
		return 2, true
	}
	return 1, false
}

// Position returns a sortable timeline position for a snapshot:
// season, stage, then week (offseason stages subdivide the offseason).
func Position(s extract.Season) (int64, bool) {
	rank, known := stageRank(s.Stage)
	week := int64(s.Week)
	if s.Stage == "OffSeason" {
		week = int64(s.OffseasonStage)*100 + int64(s.Week)
	}
	return int64(s.SeasonIndex)*1_000_000 + int64(rank)*100_000 + week, known
}

// Fingerprint hashes the results of every played game in the snapshot's
// season. Two saves on the same lineage at the same week share it.
func Fingerprint(snap *extract.Snapshot) string {
	lines := make([]string, 0, len(snap.Games))
	for _, g := range snap.Games {
		if !g.Played {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d:%d:%d:%d:%d:%d", g.SeasonIndex, g.Row, g.HomeTeamID, g.AwayTeamID, g.HomeScore, g.AwayScore))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// storedGame is the subset of a stored game used to detect contradictions.
type storedGame struct {
	Home, Away           int
	HomeScore, AwayScore int
	Played               bool
}

// contradicts reports whether the snapshot disagrees with results already
// stored for its season: a different score or teams, or a previously played
// game that is now unplayed. Games the snapshot does not mention are ignored
// (the save may have dropped them).
func contradicts(snap *extract.Snapshot, stored map[int]storedGame) (bool, string) {
	for _, g := range snap.Games {
		s, ok := stored[g.Row]
		if !ok || !s.Played {
			continue
		}
		if !g.Played {
			return true, fmt.Sprintf("game %d was played (%d-%d) and is now unplayed", g.Row, s.AwayScore, s.HomeScore)
		}
		if s.Home != g.HomeTeamID || s.Away != g.AwayTeamID || s.HomeScore != g.HomeScore || s.AwayScore != g.AwayScore {
			return true, fmt.Sprintf("game %d result changed: stored %d-%d, now %d-%d", g.Row, s.AwayScore, s.HomeScore, g.AwayScore, g.HomeScore)
		}
	}
	return false, ""
}
