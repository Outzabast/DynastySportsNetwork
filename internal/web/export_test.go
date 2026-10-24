package web

import (
	"reflect"
	"testing"
)

func TestDemoteHeadings(t *testing.T) {
	in := "# Title\n\ntext with a # inside\n## Sub\n###### Deep"
	want := "### Title\n\ntext with a # inside\n#### Sub\n###### Deep"
	if got := demoteHeadings(in); got != want {
		t.Errorf("demoteHeadings:\n got %q\nwant %q", got, want)
	}
}

func TestPlainParagraphs(t *testing.T) {
	in := "# Bobcats roll\n\nTexas State **dominated**\nfrom the start.\n\n- Run game\n- Pass | catch\n1. First"
	want := []string{"Bobcats roll", "Texas State dominated from the start.", "- Run game", "- Pass | catch", "1. First"}
	if got := plainParagraphs(in); !reflect.DeepEqual(got, want) {
		t.Errorf("plainParagraphs:\n got %q\nwant %q", got, want)
	}
}
