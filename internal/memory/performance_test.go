package memory_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/graph"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/search"
	"github.com/JayWanX/memory/internal/memory/store"
)

// N is the number of active insights, not the benchmark iteration count. Keep
// text length, vector dimension, result limits and graph degree fixed as N grows.
var corpusSizes = []int{1, 10, 100, 1000, 10000}

func corpusInsight(i int) *model.Insight {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
	return &model.Insight{
		ID:       fmt.Sprintf("ins-%06d", i),
		Content:  fmt.Sprintf("SQLite memory retrieval stores project record %06d with graph links and multilingual 查询性能", i),
		Category: model.CategoryFact, Importance: 1 + i%5,
		Tags: []string{"memory", "performance"}, Entities: []string{"SQLite", fmt.Sprintf("Project%02d", i%10)},
		Source: fmt.Sprintf("source-%02d", i%10), CreatedAt: created, UpdatedAt: created,
	}
}

func corpusVector(i int) []float64 {
	v := make([]float64, 128)
	for j := range v {
		// Deterministic, positive vectors with varied cosine scores. Round-trip
		// through the storage format so cached and stored vectors are identical.
		v[j] = float64(1+(i*31+j*17)%101) / 101
	}
	return embed.DeserializeVector(embed.SerializeVector(v))
}

type memoryCorpus struct {
	db       *store.DB
	insights []*model.Insight
	vectors  graph.EmbedCache
	embedded []search.EmbeddedItem
}

func benchmarkMemoryOperation(b *testing.B, run func() error) {
	b.Helper()
	cpuStart := processCPUTime()
	for b.Loop() {
		if err := run(); err != nil {
			b.Fatal(err)
		}
	}
	reportCPU(b, processCPUTime()-cpuStart)
}

func reportCPU(b *testing.B, elapsed time.Duration) {
	if elapsed > 0 && b.N > 0 {
		b.ReportMetric(float64(elapsed)/float64(b.N), "cpu-ns/op")
	}
}

func newMemoryCorpus(b *testing.B, n int) memoryCorpus {
	b.Helper()
	db, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	c := memoryCorpus{db: db, vectors: make(graph.EmbedCache, n)}
	err = db.InTransaction(func() error {
		for i := 0; i < n; i++ {
			ins := corpusInsight(i)
			if err := db.InsertInsight(ins); err != nil {
				return err
			}
			vec := corpusVector(i)
			if err := db.UpdateEmbedding(ins.ID, embed.SerializeVector(vec)); err != nil {
				return err
			}
			c.insights = append(c.insights, ins)
			c.vectors[ins.ID] = vec
			c.embedded = append(c.embedded, search.EmbeddedItem{ID: ins.ID, Embedding: vec})
			if i > 0 {
				if err := db.InsertEdge(&model.Edge{
					SourceID: c.insights[i-1].ID, TargetID: ins.ID,
					EdgeType: model.EdgeCausal, Weight: 0.5, CreatedAt: ins.CreatedAt,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

// BenchmarkMemoryRead measures both pure ranking and complete local read paths.
// Setup/migration, embedding providers, access-count writes and CLI startup are
// excluded. SQLite uses a real temporary WAL database, with the default indexes.
func BenchmarkMemoryRead(b *testing.B) {
	for _, n := range corpusSizes {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			c := newMemoryCorpus(b, n)
			query := "SQLite memory retrieval 查询性能"
			queryVec := corpusVector(7)
			cases := []struct {
				name string
				run  func() error
			}{
				{"ByID", func() error { _, err := c.db.GetInsightByID(c.insights[n/2].ID); return err }},
				{"AllActive", func() error { _, err := c.db.GetAllActiveInsights(); return err }},
				{"Ranked", func() error { _, err := c.db.QueryInsights(store.QueryFilter{Limit: 10}); return err }},
				{"SourceLatest", func() error { _, err := c.db.GetLatestInsightBySource("source-00", ""); return err }},
				{"SourceRecent", func() error { _, err := c.db.GetRecentInsightsBySource("source-00", "", 10); return err }},
				{"Entity", func() error { _, err := c.db.FindInsightsWithEntity("SQLite", "", 10); return err }},
				{"EntityMiss", func() error { _, err := c.db.FindInsightsWithEntity("absent", "", 10); return err }},
				{"RankedKeywordMiss", func() error {
					_, err := c.db.QueryInsights(store.QueryFilter{Keyword: "absent", Limit: 10})
					return err
				}},
				{"Embeddings", func() error { _, err := c.db.GetAllEmbeddings(); return err }},
				{"Keyword", func() error { search.KeywordSearch(c.insights, query, 10); return nil }},
				{"Diff", func() error {
					search.Diff(c.insights, query, search.DiffOptions{Limit: 5, NewEmbedding: queryVec, ExistingEmbed: c.embedded})
					return nil
				}},
				{"RecallKeyword", func() error {
					_, err := search.IntentAwareRecall(c.db, query, nil, []string{"SQLite"}, 10, nil)
					return err
				}},
				{"RecallHybrid", func() error {
					_, err := search.IntentAwareRecall(c.db, query, queryVec, []string{"SQLite"}, 10, nil)
					return err
				}},
				{"RecallWhy", func() error {
					intent := search.IntentWhy
					_, err := search.IntentAwareRecall(c.db, query, queryVec, nil, 10, &intent)
					return err
				}},
				{"SemanticEmbedding", func() error { graph.FindSemanticCandidates(c.db, c.insights[0], c.vectors); return nil }},
				{"SemanticTokens", func() error { graph.FindSemanticCandidates(c.db, c.insights[0], graph.EmbedCache{}); return nil }},
				{"Neighborhood", func() error { graph.GetNeighborhood(c.db, c.insights[0].ID, 2, 20); return nil }},
			}
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					b.ReportAllocs()
					benchmarkMemoryOperation(b, tc.run)
				})
			}
		})
	}
}

// BenchmarkMemoryInsert measures an atomic batch of N inserts including commit.
// Cleanup is outside the timer; each iteration inserts into the same empty store.
func BenchmarkMemoryInsert(b *testing.B) {
	for _, n := range corpusSizes {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			c := newMemoryCorpus(b, 0)
			insights := make([]*model.Insight, n)
			for i := range insights {
				insights[i] = corpusInsight(i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var cpuTotal time.Duration
			for i := 0; i < b.N; i++ {
				cpuStart := processCPUTime()
				if err := c.db.InTransaction(func() error {
					for _, ins := range insights {
						if err := c.db.InsertInsight(ins); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				cpuTotal += processCPUTime() - cpuStart
				b.StopTimer()
				if _, err := c.db.Conn().Exec(`DELETE FROM insights`); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
			reportCPU(b, cpuTotal)
		})
	}
}

// BenchmarkMemoryMaintenance isolates store-wide metadata scans and the local
// graph write pipeline. Existing edges are replaced on each iteration, keeping
// the corpus size bounded; these are not provider or CLI benchmarks.
func BenchmarkMemoryMaintenance(b *testing.B) {
	for _, n := range corpusSizes {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			c := newMemoryCorpus(b, n)
			b.Run("KnownEntities", func(b *testing.B) {
				benchmarkMemoryOperation(b, func() error { _, err := c.db.LoadKnownEntities(); return err })
			})
			b.Run("Retention", func(b *testing.B) {
				benchmarkMemoryOperation(b, func() error { _, _, err := c.db.GetRetentionCandidates(1, 10); return err })
			})
			b.Run("EngineProvided", func(b *testing.B) {
				engine := graph.NewEngineWithEntityMode(c.db, c.vectors, graph.EntityModeProvided)
				benchmarkMemoryOperation(b, func() error {
					return c.db.InTransaction(func() error {
						engine.OnInsightCreated(c.insights[0])
						return nil
					})
				})
			})
		})
	}
}

func BenchmarkMemoryDenseRecall(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			c := newMemoryCorpus(b, n)
			if err := c.db.InTransaction(func() error {
				for i, source := range c.insights {
					for j, target := range c.insights {
						if i == j {
							continue
						}
						if err := c.db.InsertEdge(&model.Edge{
							SourceID: source.ID, TargetID: target.ID, EdgeType: model.EdgeSemantic,
							Weight: float64(1+(i*31+j*17)%101) / 101, CreatedAt: source.CreatedAt,
						}); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			queryVec := corpusVector(7)
			benchmarkMemoryOperation(b, func() error {
				_, err := search.IntentAwareRecall(c.db, "SQLite memory", queryVec, nil, 10, nil)
				return err
			})
		})
	}
}
