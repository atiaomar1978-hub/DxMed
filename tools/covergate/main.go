// Command covergate enforces the coverage gates on a Go cover profile: 100% for the pure engine packages
// and a minimum for the module overall. Test helpers and thin command mains are excluded.
//
//	go run ./tools/covergate -profile coverage/all.out -min 95
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Strict packages must be fully covered (decision D5).
var strict = []string{"model", "datatype", "ber", "lookup", "transform", "filter", "decode", "dedupe", "route", "encode",
	"merge", "validate", "config", "pipeline", "sample", "lab", "obs"}

var excluded = []string{"dxmed/internal/dbtest/", "dxmed/cmd/", "dxmed/tools/"}

type count struct{ total, covered int }

func (c count) pct() float64 {
	if c.total == 0 {
		return 100
	}
	return 100 * float64(c.covered) / float64(c.total)
}

// read parses a profile; blocks reported several times (one per test binary) count once, covered if any run hit them.
func read(r io.Reader) (map[string]count, error) {
	blocks := map[string]bool{}
	stmts := map[string]int{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("bad profile line %q", line)
		}
		n, err1 := strconv.Atoi(f[1])
		hits, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad profile line %q", line)
		}
		stmts[f[0]] = n
		blocks[f[0]] = blocks[f[0]] || hits > 0
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	pkgs := map[string]count{}
	for b, hit := range blocks {
		file := b[:strings.LastIndex(b, ":")]
		if skip(file) {
			continue
		}
		pkg := file[:strings.LastIndex(file, "/")]
		c := pkgs[pkg]
		c.total += stmts[b]
		if hit {
			c.covered += stmts[b]
		}
		pkgs[pkg] = c
	}
	return pkgs, nil
}

func skip(file string) bool {
	for _, p := range excluded {
		if strings.HasPrefix(file, p) {
			return true
		}
	}
	return false
}

func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("covergate", flag.ContinueOnError)
	fs.SetOutput(out)
	profile := fs.String("profile", "coverage/all.out", "cover profile")
	min := fs.Float64("min", 95, "minimum overall coverage (percent)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	f, err := os.Open(*profile)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	defer f.Close()
	pkgs, err := read(f)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	names := make([]string, 0, len(pkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	var all count
	fail := false
	for _, p := range names {
		c := pkgs[p]
		all.total += c.total
		all.covered += c.covered
		mark := ""
		for _, s := range strict {
			if p == "dxmed/internal/"+s && c.covered < c.total {
				mark, fail = "  FAIL (100% required)", true
			}
		}
		fmt.Fprintf(out, "%-32s %6.1f%%%s\n", p, c.pct(), mark)
	}
	fmt.Fprintf(out, "%-32s %6.1f%% (minimum %.1f%%)\n", "total", all.pct(), *min)
	if all.pct() < *min {
		fail = true
		fmt.Fprintln(out, "FAIL: overall coverage below the minimum")
	}
	if fail {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
