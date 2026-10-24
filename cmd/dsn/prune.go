package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// pruneArchives deletes superseded raw saves from data/archive: every save
// that is not the newest one at its timeline position on its branch. scope
// limits it to one snapshot's position (0 = all). The snapshot is marked
// before its file goes, so a failure leaves a stray file, never a snapshot
// that claims an archive it doesn't have.
func pruneArchives(ctx context.Context, st *store.Store, dataDir string, scope int, dry bool) (files int, freed int64, err error) {
	list, err := st.SupersededArchives(ctx, scope)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range list {
		if dry {
			files++
			freed += archiveSize(dataDir, a.SHA)
			continue
		}
		if err := st.MarkArchivePruned(ctx, a.SHA); err != nil {
			return files, freed, err
		}
		n, err := collector.RemoveArchive(dataDir, a.SHA)
		if err != nil {
			return files, freed, err
		}
		files++
		freed += n
	}
	return files, freed, nil
}

// archiveSize is the size of a save's archive file (0 when it's missing).
func archiveSize(dataDir, sha string) int64 {
	info, err := os.Stat(filepath.Join(dataDir, "archive", sha+".gz"))
	if err != nil {
		return 0
	}
	return info.Size()
}

func runPruneArchive(args []string) error {
	var c config
	fs := flag.NewFlagSet("prune-archive", flag.ExitOnError)
	c.bind(fs)
	dry := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()
	files, freed, err := pruneArchives(ctx, st, c.DataDir, 0, *dry)
	verb := "deleted"
	if *dry {
		verb = "would delete"
	}
	fmt.Printf("%s %d superseded save(s), %.0f MB\n", verb, files, float64(freed)/1e6)
	return err
}
