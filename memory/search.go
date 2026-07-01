package memory

import (
	"sort"
	"strings"
)

// defaultRecall is how many memories Search returns when no limit is given.
const defaultRecall = 5

// Search returns the memories most relevant to query, best match first, capped
// at limit (limit <= 0 uses defaultRecall). Relevance is keyword overlap —
// distinct query terms found in a memory, weighted higher for a name match.
// It's deterministic and offline (no embeddings): good enough for a personal
// note store, and swappable for a vector index later behind this signature.
func (s *Store) Search(query string, limit int) ([]Memory, error) {
	mems, err := s.Load()
	if err != nil {
		return nil, err
	}
	return rank(mems, query, limit), nil
}

func rank(mems []Memory, query string, limit int) []Memory {
	if limit <= 0 {
		limit = defaultRecall
	}
	qterms := terms(query)
	if len(qterms) == 0 {
		return nil
	}
	type scored struct {
		m Memory
		s int
	}
	var hits []scored
	for _, m := range mems {
		nameToks := terms(m.Name)
		bodyToks := terms(m.Content)
		score := 0
		for qt := range qterms {
			switch {
			case hasTerm(nameToks, qt):
				score += 2
			case hasTerm(bodyToks, qt):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{m, score})
		}
	}
	// Highest score first; ties broken by name for stable, predictable output.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].m.Name < hits[j].m.Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Memory, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out
}

// hasTerm reports whether a memory's token set matches query term qt: an exact
// token, or a prefix either way for terms of 4+ runes (so "project" matches
// "projects", and "proj" matches "project") — light stemming without a stemmer.
func hasTerm(set map[string]bool, qt string) bool {
	if set[qt] {
		return true
	}
	if len(qt) < 4 {
		return false
	}
	for tok := range set {
		if strings.HasPrefix(tok, qt) || strings.HasPrefix(qt, tok) {
			return true
		}
	}
	return false
}

// terms splits text into a set of distinct lowercased word tokens, dropping
// single characters and common stop words so scoring keys on meaningful words.
func terms(text string) map[string]bool {
	set := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(f) < 2 || stop[f] {
			continue
		}
		set[f] = true
	}
	return set
}

// stop is a small stop-word set — high-frequency words that carry little
// retrieval signal, kept short on purpose (this isn't a search engine).
var stop = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "was": true,
	"with": true, "that": true, "this": true, "you": true, "your": true,
	"about": true, "what": true, "did": true, "does": true, "has": true,
}
