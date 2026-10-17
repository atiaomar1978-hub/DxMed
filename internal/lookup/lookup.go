// Package lookup holds in-memory lookup tables: exact keys and longest-prefix matching
// (Readg VALIN / VALDB / LOOKP, number plans, MCC-MNC, MVNO ranges).
package lookup

import (
	"fmt"
	"sort"
)

// Def is the configuration of a lookup table.
type Def struct {
	Name string            `json:"name"`
	Kind string            `json:"kind"` // set | map | prefix
	Rows map[string]string `json:"rows"` // key -> value (value ignored for set)
}

// Table is a compiled lookup table.
type Table struct {
	Name string
	Kind string
	rows map[string]string
	trie *node
}

type node struct {
	kids  map[byte]*node
	val   string
	isEnd bool
}

// Compile builds a table from its definition.
func Compile(d Def) (*Table, error) {
	switch d.Kind {
	case "set", "map", "prefix":
	default:
		return nil, fmt.Errorf("lookup %q: kind must be set, map or prefix", d.Name)
	}
	t := &Table{Name: d.Name, Kind: d.Kind, rows: make(map[string]string, len(d.Rows)), trie: &node{}}
	for k, v := range d.Rows {
		t.rows[k] = v
		n := t.trie
		for i := 0; i < len(k); i++ {
			if n.kids == nil {
				n.kids = map[byte]*node{}
			}
			c := n.kids[k[i]]
			if c == nil {
				c = &node{}
				n.kids[k[i]] = c
			}
			n = c
		}
		n.isEnd, n.val = true, v
	}
	return t, nil
}

// Len is the number of rows.
func (t *Table) Len() int { return len(t.rows) }

// Contains reports an exact key match.
func (t *Table) Contains(key string) bool {
	_, ok := t.rows[key]
	return ok
}

// Get returns the value for an exact key.
func (t *Table) Get(key string) (string, bool) {
	v, ok := t.rows[key]
	return v, ok
}

// LongestPrefix returns the value of the longest key that is a prefix of s.
func (t *Table) LongestPrefix(s string) (key, val string, ok bool) {
	n := t.trie
	if n.isEnd {
		key, val, ok = "", n.val, true
	}
	for i := 0; i < len(s); i++ {
		n = n.kids[s[i]]
		if n == nil {
			break
		}
		if n.isEnd {
			key, val, ok = s[:i+1], n.val, true
		}
	}
	return key, val, ok
}

// Resolve applies the table's natural match: prefix tables use longest prefix, others exact keys.
func (t *Table) Resolve(s string) (string, bool) {
	if t.Kind == "prefix" {
		_, v, ok := t.LongestPrefix(s)
		return v, ok
	}
	return t.Get(s)
}

// Keys returns the sorted keys (UI export).
func (t *Table) Keys() []string {
	out := make([]string, 0, len(t.rows))
	for k := range t.rows {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Registry is a set of compiled tables by name.
type Registry map[string]*Table

// Build compiles all definitions.
func Build(defs []Def) (Registry, error) {
	r := Registry{}
	for _, d := range defs {
		if _, dup := r[d.Name]; dup {
			return nil, fmt.Errorf("lookup %q defined twice", d.Name)
		}
		t, err := Compile(d)
		if err != nil {
			return nil, err
		}
		r[d.Name] = t
	}
	return r, nil
}

// Func returns a datatype-compatible lookup function.
func (r Registry) Func() func(table, key string) (string, bool) {
	return func(table, key string) (string, bool) {
		t, ok := r[table]
		if !ok {
			return "", false
		}
		return t.Resolve(key)
	}
}
