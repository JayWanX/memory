package graph

import (
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/internal/topk"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/search"
	"github.com/JayWanX/memory/internal/memory/store"
)

// Minimum similarity to be considered a semantic candidate (token overlap fallback).
const minSemanticSimilarity = 0.10

// Minimum cosine similarity to appear as a review candidate (grey zone lower bound).
const reviewSemanticThreshold = 0.40

// Minimum cosine similarity to auto-create a semantic edge (high confidence).
const autoSemanticThreshold = 0.80

// Maximum number of semantic candidates to return.
const maxSemanticCandidates = 5

// Maximum number of auto-created semantic edges per insight.
const maxAutoSemanticEdges = 3

// SemanticCandidate represents a potential semantic link for Claude to evaluate.
// When AutoLinked is true, the edge was already created automatically (high confidence).
// When false, the candidate is in the review zone and needs LLM judgment.
type SemanticCandidate struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Category   string  `json:"category"`
	Similarity float64 `json:"similarity"`
	AutoLinked bool    `json:"auto_linked"`
}

// EmbedCache maps insight ID → deserialized embedding vector.
// When passed as nil, functions fall back to loading from the database.
type EmbedCache map[string][]float64

// buildEmbedCache loads all embeddings from DB into a map.
func buildEmbedCache(db *store.DB) EmbedCache {
	allEmbedded, err := db.GetAllEmbeddings()
	if err != nil || len(allEmbedded) == 0 {
		return nil
	}
	cache := make(EmbedCache, len(allEmbedded))
	for _, e := range allEmbedded {
		if v := embed.DeserializeVector(e.Embedding); v != nil {
			cache[e.ID] = v
		}
	}
	return cache
}

// CreateSemanticEdges auto-creates semantic edges for insights with high
// embedding cosine similarity (MAGMA §3.2: cos(v_i, v_j) > θ_sim).
// If embedCache is non-nil, it is used instead of querying the database.
// Returns the number of edges created.
func CreateSemanticEdges(db *store.DB, insight *model.Insight, embedCache EmbedCache) int {
	if embedCache == nil {
		embedCache = buildEmbedCache(db)
	}

	insightVec := embedCache[insight.ID]
	if insightVec == nil {
		return 0
	}

	candidates := semanticTopK(embedCache, insight.ID, insightVec, autoSemanticThreshold, maxAutoSemanticEdges)
	if len(candidates) == 0 {
		return 0
	}

	now := time.Now().UTC()
	count := 0
	for _, c := range candidates {
		// Bidirectional semantic edges (MAGMA: undirected)
		meta := map[string]string{
			"created_by": "auto",
			"cosine":     fmt.Sprintf("%.4f", c.similarity),
		}
		err1 := db.InsertEdge(&model.Edge{
			SourceID: insight.ID, TargetID: c.id,
			EdgeType: model.EdgeSemantic, Weight: c.similarity,
			Metadata: meta, CreatedAt: now,
		})
		err2 := db.InsertEdge(&model.Edge{
			SourceID: c.id, TargetID: insight.ID,
			EdgeType: model.EdgeSemantic, Weight: c.similarity,
			Metadata: meta, CreatedAt: now,
		})
		if err1 == nil {
			count++
		}
		if err2 == nil {
			count++
		}
	}
	return count
}

// FindSemanticCandidates returns insights that are potential semantic matches
// for the given insight. When embeddings are available, uses cosine similarity
// (MAGMA-compliant); falls back to token overlap otherwise.
// If embedCache is non-nil, it is used instead of querying the database.
// These are candidates only — Claude evaluates and creates actual semantic
// edges via `memory link`.
func FindSemanticCandidates(db *store.DB, insight *model.Insight, embedCache EmbedCache) []SemanticCandidate {
	if embedCache == nil {
		embedCache = buildEmbedCache(db)
	}
	// Try embedding-based candidates first (P4: MAGMA compliance)
	if candidates := findCandidatesByEmbedding(db, insight, embedCache); candidates != nil {
		return candidates
	}
	// Fallback: token overlap
	return findCandidatesByTokenOverlap(db, insight)
}

// findCandidatesByEmbedding uses cosine similarity over the embed cache.
// Candidates with cosine >= autoSemanticThreshold are marked as auto-linked.
// Candidates in [reviewSemanticThreshold, autoSemanticThreshold) need LLM review.
func findCandidatesByEmbedding(db *store.DB, insight *model.Insight, embedCache EmbedCache) []SemanticCandidate {
	insightVec := embedCache[insight.ID]
	if insightVec == nil {
		return nil
	}

	candidates := semanticTopK(embedCache, insight.ID, insightVec, reviewSemanticThreshold, maxSemanticCandidates)
	if len(candidates) == 0 {
		return nil
	}

	result := make([]SemanticCandidate, 0, len(candidates))
	for _, c := range candidates {
		ins, err := db.GetInsightByID(c.id)
		if err != nil || ins == nil {
			continue // soft-deleted or missing — skip
		}
		result = append(result, SemanticCandidate{
			ID:         ins.ID,
			Content:    ins.Content,
			Category:   string(ins.Category),
			Similarity: c.similarity,
			AutoLinked: c.similarity >= autoSemanticThreshold,
		})
	}
	if len(result) == 0 {
		return nil // all candidates filtered out — allow token overlap fallback
	}
	return result
}

// findCandidatesByTokenOverlap is the fallback when embeddings are unavailable.
func findCandidatesByTokenOverlap(db *store.DB, insight *model.Insight) []SemanticCandidate {
	all, err := db.GetAllActiveInsights()
	if err != nil || len(all) == 0 {
		return nil
	}

	type scored struct {
		insight    *model.Insight
		similarity float64
	}

	best := topk.New(maxSemanticCandidates, func(a, b scored) bool { return a.similarity > b.similarity })
	queryTokens := search.Tokenize(insight.Content)
	for _, other := range all {
		if other.ID == insight.ID {
			continue
		}
		sim := search.TokenOverlapSimilarity(queryTokens, search.Tokenize(other.Content))
		if sim >= minSemanticSimilarity {
			best.Add(scored{insight: other, similarity: sim})
		}
	}
	candidates := best.Sorted()

	result := make([]SemanticCandidate, len(candidates))
	for i, c := range candidates {
		result[i] = SemanticCandidate{
			ID:         c.insight.ID,
			Content:    c.insight.Content,
			Category:   string(c.insight.Category),
			Similarity: c.similarity,
		}
	}
	return result
}

type semanticScore struct {
	id         string
	similarity float64
}

// semanticTopK performs exact cosine scoring, keeping only the bounded output.
// Filtering active/missing candidates still occurs after selection, preserving
// the caller's existing fallback and edge-write behavior.
func semanticTopK(cache EmbedCache, excludeID string, vec []float64, threshold float64, limit int) []semanticScore {
	best := topk.New(limit, func(a, b semanticScore) bool { return a.similarity > b.similarity })
	for id, other := range cache {
		if id == excludeID {
			continue
		}
		if sim := embed.CosineSimilarity(vec, other); sim >= threshold {
			best.Add(semanticScore{id: id, similarity: sim})
		}
	}
	return best.Sorted()
}
