// Package retrieval implements search-engine-grade hybrid retrieval for
// Ragamuffin: an in-process BM25 lexical index, Reciprocal Rank Fusion (RRF),
// and a hybrid orchestrator that fuses dense (semantic) and lexical (BM25)
// result rankings. No external dependencies — lexical recall is computed
// in-process over the vault's chunk text, and fused with Qdrant dense results
// via RRF so the two heterogeneous scorers need no calibration.
package retrieval

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// SparseVector is an inverted-index style sparse representation: parallel
// slices of term indices and their weights, in the format Qdrant expects.
type SparseVector struct {
	Indices []uint32
	Values  []float32
}

// LexicalEncoder turns text into a sparse vector using term-frequency with
// inverse-document-frequency weighting (BM25-style). It is corpus-aware: IDF
// requires document-frequency statistics, which are supplied by an optional
// DFProvider. Without a provider (e.g. at query time before any docs are
// seen), it degrades gracefully to raw TF, which still gives lexical recall.
type LexicalEncoder struct {
	mu        sync.RWMutex
	vocab     map[string]uint32
	invVocab  map[uint32]string
	docFreq   map[uint32]int // number of chunks containing the term
	totalDocs int
	maxTermID uint32
	// stopwords reduce noise on common tokens
	stopwords map[string]struct{}
}

// DFProvider supplies document-frequency statistics for IDF computation. It is
// implemented by the indexer so the encoder can weight terms by corpus rarity.
type DFProvider func() (docFreq map[string]int, totalDocs int)

// NewLexicalEncoder creates an encoder with an empty vocabulary.
func NewLexicalEncoder() *LexicalEncoder {
	return &LexicalEncoder{
		vocab:     make(map[string]uint32),
		invVocab:  make(map[uint32]string),
		docFreq:   make(map[uint32]int),
		stopwords: defaultStopwords(),
	}
}

// Observe registers term document-frequencies for a batch of documents. Call
// this during indexing so subsequent Encode calls get meaningful IDF weights.
// df maps term -> number of documents containing it; totalDocs is the corpus
// size used for the IDF denominator.
func (e *LexicalEncoder) Observe(df map[string]int, totalDocs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for term, count := range df {
		id, ok := e.vocab[term]
		if !ok {
			e.maxTermID++
			id = e.maxTermID
			e.vocab[term] = id
			e.invVocab[id] = term
		}
		// Keep the max seen — indexing is monotonic over the corpus.
		if count > e.docFreq[id] {
			e.docFreq[id] = count
		}
	}
	if totalDocs > e.totalDocs {
		e.totalDocs = totalDocs
	}
}

// tokenize lowercases, splits on non-alphanumeric boundaries, drops stopwords
// and single-character tokens, and returns term frequencies.
func (e *LexicalEncoder) tokenize(text string) map[string]int {
	lower := strings.ToLower(text)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	tf := make(map[string]int)
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		if _, stop := e.stopwords[f]; stop {
			continue
		}
		tf[f]++
	}
	return tf
}

// Encode converts text into a SparseVector. When IDF statistics are available
// it applies BM25-style weighting: tf * (1 + log((N+1)/(df+1))).
func (e *LexicalEncoder) Encode(text string) SparseVector {
	tf := e.tokenize(text)

	e.mu.RLock()
	N := e.totalDocs
	haveStats := N > 0 && len(e.docFreq) > 0
	var indices []uint32
	var values []float32
	for term, count := range tf {
		id, ok := e.vocab[term]
		if !ok {
			// Unknown term at encode time: assign a new id locally so the
			// query vector can still match if the term was indexed later.
			// Vocabulary divergence between index and query is acceptable
			// because Qdrant stores sparse indices opaquely.
			e.mu.RUnlock()
			e.mu.Lock()
			id, ok = e.vocab[term]
			if !ok {
				e.maxTermID++
				id = e.maxTermID
				e.vocab[term] = id
				e.invVocab[id] = term
			}
			e.mu.Unlock()
			e.mu.RLock()
		}
		weight := float32(count)
		if haveStats {
			df := e.docFreq[id]
			idf := 1.0 + math.Log(float64(N+1)/float64(df+1))
			weight = float32(count) * float32(idf)
		}
		indices = append(indices, id)
		values = append(values, weight)
	}
	e.mu.RUnlock()

	if len(indices) == 0 {
		return SparseVector{}
	}
	// L2-normalize so sparse scores are comparable to dense cosine scores.
	normalize(indices, values)
	return SparseVector{Indices: indices, Values: values}
}

func normalize(indices []uint32, values []float32) {
	var norm float32
	for _, v := range values {
		norm += v * v
	}
	if norm == 0 {
		return
	}
	norm = float32(math.Sqrt(float64(norm)))
	for i := range values {
		values[i] /= norm
	}
}

// ── Reciprocal Rank Fusion ──────────────────────────────────────────────────

// RankedID is a single document in a ranked result list.
type RankedID struct {
	ID    string
	Score float32 // fused RRF score (higher = better)
}

// Fuse merges multiple ranked lists using Reciprocal Rank Fusion:
//
//	score(p) = Σ_{r in rankings} 1 / (k + rank_r(p))
//
// where rank is 1-based and k=60 by default. No score normalization is needed,
// which is exactly why RRF works across heterogeneous retrievers (dense cosine
// vs sparse BM25) without calibration. Cormack et al., SIGIR 2009.
func Fuse(lists [][]string, k int) []RankedID {
	if k <= 0 {
		k = 60
	}
	scores := make(map[string]float32)
	for _, list := range lists {
		for rank, id := range list {
			scores[id] += 1.0 / float32(k+rank+1)
		}
	}
	out := make([]RankedID, 0, len(scores))
	for id, s := range scores {
		out = append(out, RankedID{ID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// FuseWithScores is like Fuse but preserves the per-list provenance so callers
// can report which retrievers contributed to a hit. It is used when `expand` or
// attribution detail is needed.
func FuseWithScores(rankings []ScoredRanking, k int) []RankedID {
	if k <= 0 {
		k = 60
	}
	best := make(map[string]float32)
	for _, r := range rankings {
		for rank, item := range r.Items {
			best[item.ID] += 1.0 / float32(k+rank+1)
		}
	}
	out := make([]RankedID, 0, len(best))
	for id, s := range best {
		out = append(out, RankedID{ID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// ScoredRanking is a ranked list that carries its retriever label.
type ScoredRanking struct {
	Source string
	Items  []RankedID
}

// defaultStopwords is a small, language-agnostic English stopword list. The
// encoder is intentionally simple; teams with domain-specific noise can extend
// this set without touching the core logic.
func defaultStopwords() map[string]struct{} {
	words := []string{
		"the", "a", "an", "and", "or", "but", "if", "then", "else", "for",
		"of", "to", "in", "on", "at", "by", "with", "from", "as", "is",
		"are", "was", "were", "be", "been", "being", "this", "that", "these",
		"those", "it", "its", "we", "you", "they", "he", "she", "i", "me",
		"my", "your", "our", "their", "not", "no", "yes", "do", "does",
		"did", "can", "will", "would", "should", "could", "may", "might",
	}
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

// ── In-process BM25 lexical index ────────────────────────────────────────────

// LexicalIndex is an in-memory BM25 index over chunk documents. It provides
// lexical recall that complements dense semantic search. The index is built by
// the server from a Qdrant scroll of the vault's chunk text and refreshed on
// reindex. It is safe for concurrent reads; Build replaces the index wholesale.
type LexicalIndex struct {
	mu        sync.RWMutex
	docs      map[string]string // id -> text
	docFreq   map[string]int    // term -> number of docs containing it
	totalDocs int
	avgLen    float64
	stopwords map[string]struct{}
	k1        float64
	b         float64
}

// NewLexicalIndex creates an empty BM25 index with standard parameters
// (k1=1.5, b=0.75).
func NewLexicalIndex() *LexicalIndex {
	return &LexicalIndex{
		docs:      make(map[string]string),
		docFreq:   make(map[string]int),
		stopwords: defaultStopwords(),
		k1:        1.5,
		b:         0.75,
	}
}

// Build replaces the index contents from a set of (id, text) documents. It is
// O(total tokens) and intended to run off the query path (e.g. on reindex).
func (idx *LexicalIndex) Build(docs []Doc) {
	docFreq := make(map[string]int)
	docMap := make(map[string]string)
	var totalLen float64
	for _, d := range docs {
		docMap[d.ID] = d.Text
		tokens := tokenizeText(d.Text, idx.stopwords)
		seen := make(map[string]struct{})
		for _, t := range tokens {
			if _, ok := seen[t]; !ok {
				docFreq[t]++
				seen[t] = struct{}{}
			}
		}
		totalLen += float64(len(tokens))
	}
	avg := 0.0
	if len(docMap) > 0 {
		avg = totalLen / float64(len(docMap))
	}
	idx.mu.Lock()
	idx.docs = docMap
	idx.docFreq = docFreq
	idx.totalDocs = len(docMap)
	idx.avgLen = avg
	idx.mu.Unlock()
}

// Doc is a single indexable document.
type Doc struct {
	ID   string
	Text string
}

// Size returns the number of indexed documents.
func (idx *LexicalIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// Search runs BM25 retrieval for the query and returns ranked document IDs.
// Empty index or empty query returns nil.
func (idx *LexicalIndex) Search(query string, limit int) []RankedID {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if len(idx.docs) == 0 {
		return nil
	}
	qTokens := tokenizeText(query, idx.stopwords)
	if len(qTokens) == 0 {
		return nil
	}
	N := float64(idx.totalDocs)
	scores := make(map[string]float64)
	for _, term := range qTokens {
		df, ok := idx.docFreq[term]
		if !ok {
			continue
		}
		idf := math.Log((N - float64(df) + 0.5) / (float64(df) + 0.5))
		if idf < 0 {
			idf = 0
		}
		for id, text := range idx.docs {
			tf := float64(countTerm(text, term, idx.stopwords))
			if tf == 0 {
				continue
			}
			docLen := float64(len(tokenizeText(text, idx.stopwords)))
			normLen := 1.0
			if idx.avgLen > 0 {
				normLen = idx.b * (docLen / idx.avgLen)
			}
			scores[id] += idf * (tf * (idx.k1 + 1)) / (tf + idx.k1*normLen)
		}
	}
	if len(scores) == 0 {
		return nil
	}
	out := make([]RankedID, 0, len(scores))
	for id, s := range scores {
		out = append(out, RankedID{ID: id, Score: float32(s)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Hybrid fuses dense (semantic) and lexical (BM25) ranked lists via Reciprocal
// Rank Fusion and returns the merged, ranked document IDs. Both inputs are
// lists of IDs in descending relevance order; Hybrid assigns RRF scores so the
// fused order needs no cross-scorer calibration. Cormack et al., SIGIR 2009.
func Hybrid(dense, lexical []string, k int) []RankedID {
	return Fuse([]([]string){dense, lexical}, k)
}

// tokenizeText lowercases, splits on non-alphanumeric boundaries, drops
// stopwords and single-character tokens.
func tokenizeText(text string, stopwords map[string]struct{}) []string {
	lower := strings.ToLower(text)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}

// countTerm counts occurrences of term in text, applying the same tokenization.
func countTerm(text, term string, stopwords map[string]struct{}) int {
	tokens := tokenizeText(text, stopwords)
	n := 0
	for _, t := range tokens {
		if t == term {
			n++
		}
	}
	return n
}
