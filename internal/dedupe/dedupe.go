// Package dedupe implements file-level (Readg CDR_BIAUTO_DUP) and record-level duplicate detection.
package dedupe

import (
	"encoding/hex"
	"fmt"
	"time"

	"dxmed/internal/model"
	"github.com/zeebo/xxh3"
)

// Key is a 128-bit record key hash.
type Key [16]byte

func (k Key) String() string { return hex.EncodeToString(k[:]) }

// RecordKey hashes the stream and the configured field values.
func RecordKey(r *model.Record, fields []string) Key {
	h := xxh3.New()
	_, _ = h.WriteString(r.Stream)
	for _, f := range fields {
		_, _ = h.Write([]byte{0x1F})
		_, _ = h.WriteString(r.Get(f).String())
	}
	return h.Sum128().Bytes()
}

// Set is an in-memory key set (within one file).
type Set map[Key]struct{}

// Add returns false when the key was already present.
func (s Set) Add(k Key) bool {
	if _, ok := s[k]; ok {
		return false
	}
	s[k] = struct{}{}
	return true
}

// FileInfo is what is known about a file for file-level checks.
type FileInfo struct {
	ID       string    `json:"id"`
	Source   string    `json:"source"`
	Name     string    `json:"name"`
	SHA256   string    `json:"sha256"`
	Exchange string    `json:"exchange"`
	SeqFrom  int64     `json:"seq_from"`
	SeqTo    int64     `json:"seq_to"`
	TimeFrom time.Time `json:"time_from"`
	TimeTo   time.Time `json:"time_to"`
}

// Policy selects which file checks apply.
type Policy struct {
	Name      bool `json:"name"`
	Checksum  bool `json:"checksum"`
	SeqRange  bool `json:"seq_range"`
	TimeRange bool `json:"time_range"`
	Gap       bool `json:"gap"`
}

// Verdict is the result of a file check.
type Verdict struct {
	Duplicate bool   `json:"duplicate"`
	Reason    string `json:"reason,omitempty"`
	Against   string `json:"against,omitempty"`
	AgainstID string `json:"against_id,omitempty"`
	GapFrom   int64  `json:"gap_from,omitempty"`
	GapTo     int64  `json:"gap_to,omitempty"`
}

// CheckFile compares a file with previously committed files.
func CheckFile(f FileInfo, prior []FileInfo, p Policy) Verdict {
	var maxSeq int64
	for _, o := range prior {
		if o.ID != "" && o.ID == f.ID {
			continue
		}
		switch {
		case p.Name && o.Source == f.Source && o.Name == f.Name:
			return Verdict{Duplicate: true, Reason: "NAME", Against: o.Name, AgainstID: o.ID}
		case p.Checksum && f.SHA256 != "" && o.SHA256 == f.SHA256:
			return Verdict{Duplicate: true, Reason: "CHECKSUM", Against: o.Name, AgainstID: o.ID}
		}
		if o.Exchange != f.Exchange {
			continue
		}
		if p.SeqRange && f.SeqTo > 0 && o.SeqTo > 0 && f.SeqFrom <= o.SeqTo && o.SeqFrom <= f.SeqTo {
			return Verdict{Duplicate: true, Reason: "SEQ_RANGE", Against: o.Name, AgainstID: o.ID}
		}
		if p.TimeRange && !f.TimeTo.IsZero() && !o.TimeTo.IsZero() && f.TimeFrom.Before(o.TimeTo) && o.TimeFrom.Before(f.TimeTo) {
			return Verdict{Duplicate: true, Reason: "TIME_RANGE", Against: o.Name, AgainstID: o.ID}
		}
		maxSeq = max(maxSeq, o.SeqTo)
	}
	if p.Gap && maxSeq > 0 && f.SeqFrom > maxSeq+1 {
		return Verdict{GapFrom: maxSeq + 1, GapTo: f.SeqFrom - 1}
	}
	return Verdict{}
}

// Describe renders a verdict for logs and the UI.
func (v Verdict) Describe() string {
	switch {
	case v.Duplicate:
		return fmt.Sprintf("duplicate (%s) of %s", v.Reason, v.Against)
	case v.GapTo > 0:
		return fmt.Sprintf("sequence gap %d-%d", v.GapFrom, v.GapTo)
	}
	return "ok"
}
