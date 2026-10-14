// Command tabledump lists raw save tables and dumps selected tables as JSON lines,
// for schema exploration only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/leaguelines/cfb-dynasty/dynasty"
)

func main() {
	schemaDir := flag.String("schema-dir", "data/schemas", "schema bundle directory")
	tables := flag.String("tables", "", "comma-separated table names to dump rows for")
	limit := flag.Int("limit", 5, "max rows per table (0=all)")
	flag.Parse()
	settings := dynasty.DefaultSettings()
	settings.SchemaDir = *schemaDir
	settings.AutoParse = true
	f, err := dynasty.Open(flag.Arg(0), &settings)
	if err != nil {
		log.Fatal(err)
	}
	if *tables == "" {
		for _, t := range f.Tables() {
			fields := []string{}
			if t.Schema != nil {
				for _, a := range t.Schema.Attributes {
					fields = append(fields, a.Name)
				}
			}
			fmt.Printf("%s\tid=%d\tactive=%d\talloc=%d\tarray=%v\t%s\n", t.Name(), t.Header.TableID, t.ActiveRecordCount(), t.AllocatedRecordCount(), t.Header.IsArray, strings.Join(fields, ","))
		}
		return
	}
	enc := json.NewEncoder(os.Stdout)
	for _, name := range strings.Split(*tables, ",") {
		t, ok := f.PrimaryTableByName(name)
		if !ok {
			fmt.Println("missing", name)
			continue
		}
		if err := t.ReadRecords(); err != nil {
			log.Println(name, err)
			continue
		}
		fmt.Printf("### %s id=%d rows=%d\n", name, t.Header.TableID, len(t.Records))
		for i, r := range t.Records {
			if *limit > 0 && i >= *limit {
				break
			}
			row := map[string]any{"_row": r.Index}
			keys := make([]string, 0, len(r.Fields))
			for k := range r.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := r.Fields[k]
				switch {
				case v.Reference != nil:
					row[k] = fmt.Sprintf("@%d:%d", v.Reference.TableID, v.Reference.RowNumber)
				case v.String != "":
					row[k] = v.String
				case v.Float != 0:
					row[k] = v.Float
				case v.Bool:
					row[k] = true
				default:
					row[k] = v.Int
				}
			}
			enc.Encode(row)
		}
	}
}
