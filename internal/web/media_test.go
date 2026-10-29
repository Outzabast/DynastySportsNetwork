package web

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The hosted site indexes R2's manifest; it must resolve exactly what the
// disk index resolves for the same files.
func TestManifestIndexMatchesDisk(t *testing.T) {
	paths := []string{
		"logos-flat-dark/tlow_alabama.webp",
		"3d_logos/png_OL/Akron_OL.webp",
		"helmet/left/thel_lthelmets_alabama_result.webp",
		"bowlgames/bowl_rose.webp",
		"playerportrait/nilpp_123.webp",
		"coaches/nilcp_45.webp",
		"All Logos/Conferences/SEC/a.webp",
		"All Logos/Conferences/SEC/b.webp",
	}
	dir := t.TempDir()
	for _, p := range paths {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	disk, man := newMediaIndex(dir), newManifestIndex(paths)
	disk.dir = ""
	if !reflect.DeepEqual(disk, man) {
		t.Fatalf("manifest index differs from disk index\ndisk: %+v\nman:  %+v", disk, man)
	}
	if got := man.TeamLogo("Akron"); got != "/media/3d_logos/png_OL/Akron_OL.webp" {
		t.Errorf("TeamLogo(Akron) = %q", got)
	}
	if got := man.confs["sec"]; got != "All Logos/Conferences/SEC/b.webp" {
		t.Errorf("SEC conference logo = %q", got)
	}
}
