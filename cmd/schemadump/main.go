// Command schemadump prints field names and types for named tables (exploration only).
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/leaguelines/cfb-dynasty/dynasty"
)

func main() {
	tables := flag.String("tables", "", "comma-separated table names")
	flag.Parse()
	s := dynasty.DefaultSettings()
	s.SchemaDir = "data/schemas"
	s.AutoParse = true
	f, err := dynasty.Open(flag.Arg(0), &s)
	if err != nil {
		log.Fatal(err)
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*tables, ",") {
		want[n] = true
	}
	for _, t := range f.Tables() {
		if !want[t.Name()] {
			continue
		}
		fmt.Printf("## %s id=%d active=%d\n", t.Name(), t.Header.TableID, t.ActiveRecordCount())
		if t.Schema != nil {
			for _, a := range t.Schema.Attributes {
				fmt.Printf("   %s:%s", a.Name, a.Type)
			}
			fmt.Println()
		}
	}
}
