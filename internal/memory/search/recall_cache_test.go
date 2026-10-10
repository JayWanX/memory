package search

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/model"
)

func TestCachedBeamMatchesReference(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	rng := rand.New(rand.NewSource(23))
	vectors := make(map[string][]float64)
	const n = 30
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n-%02d", i)
		insertInsight(t, db, id, id, "test", 3, nil, now)
		vec := []float64{rng.Float64(), rng.Float64(), rng.Float64()}
		blob := embed.SerializeVector(vec)
		vectors[id] = embed.DeserializeVector(blob)
		if err := db.UpdateEmbedding(id, blob); err != nil {
			t.Fatal(err)
		}
	}
	types := []model.EdgeType{model.EdgeTemporal, model.EdgeSemantic, model.EdgeCausal, model.EdgeEntity}
	for i := range 120 {
		e := &model.Edge{
			SourceID: fmt.Sprintf("n-%02d", rng.Intn(n)), TargetID: fmt.Sprintf("n-%02d", rng.Intn(n)),
			EdgeType: types[rng.Intn(len(types))], Weight: rng.Float64(), CreatedAt: now,
		}
		if i%3 == 0 {
			e.Weight = 0.5 // Exercise upstream's deterministic transition tie ordering.
		}
		if err := db.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	// Keep legacy edges pointing at a soft-deleted node: traversal may still
	// score it, but it must never be returned as an active recall candidate.
	if _, err := db.Conn().Exec(`UPDATE insights SET deleted_at = ? WHERE id = 'n-29'`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	delete(vectors, "n-29")
	all, err := db.GetAllActiveInsights()
	if err != nil {
		t.Fatal(err)
	}
	for _, queryVec := range [][]float64{nil, {0.1, 0.5, 0.9}, {1, 0}, {0, 0, 0}, {-0.1, -0.5, -0.9}} {
		for _, intent := range []Intent{IntentGeneral, IntentWhy, IntentWhen, IntentEntity} {
			for _, params := range []TraversalParams{{1, 1, 2}, {3, 4, 12}, {10, 5, 100}} {
				cache := newRecallCache(db, all, queryVec, GetWeights(intent))
				gotScores, wantScores := map[string]float64{}, map[string]float64{}
				gotVia, wantVia := map[string]string{}, map[string]string{}
				wantInsights := map[string]*model.Insight{}
				wantActive := make(map[string]*model.Insight, len(all))
				for _, ins := range all {
					wantActive[ins.ID] = ins
				}
				wantTransitions := make(map[string][]referenceRecallTransition)
				for _, anchor := range []string{"n-00", "n-01", "n-07", "n-00"} {
					cache.beamSearchFromAnchor(anchor, 1, params, gotScores, gotVia)
					referenceBeamSearch(db, anchor, 1, queryVec, GetWeights(intent), params, wantScores, wantVia, wantInsights, vectors, wantActive, wantTransitions)
				}
				if !reflect.DeepEqual(gotScores, wantScores) || !reflect.DeepEqual(gotVia, wantVia) {
					t.Fatalf("intent=%s params=%+v query=%v: cached scores/via differ from reference", intent, params, queryVec)
				}
				if cache.insights["n-29"] != nil {
					t.Fatal("deleted candidate included")
				}
			}
		}
	}
}

func TestRecallSignalsRefreshBetweenQueries(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	insertInsight(t, db, "one", "unrelated content", "test", 3, []string{"entity-only"}, now)
	for _, tc := range []struct {
		stored, query []float64
		want          float64
	}{{[]float64{1, 0}, []float64{1, 0}, 1}, {[]float64{0, 1}, []float64{1, 0}, 0}, {[]float64{0, 1}, []float64{0, 1}, 1}} {
		if err := db.UpdateEmbedding("one", embed.SerializeVector(tc.stored)); err != nil {
			t.Fatal(err)
		}
		resp, err := IntentAwareRecall(db, "entity-only", tc.query, nil, 10, nil)
		if err != nil || len(resp.Results) != 1 {
			t.Fatalf("recall: %+v, %v", resp, err)
		}
		if r := resp.Results[0]; r.Signals.Similarity != tc.want || r.Signals.Keyword != 1 {
			t.Fatalf("signals did not refresh: %+v", r.Signals)
		}
	}
	if err := db.SoftDeleteInsight("one"); err != nil {
		t.Fatal(err)
	}
	resp, err := IntentAwareRecall(db, "entity-only", []float64{0, 1}, nil, 10, nil)
	if err != nil || len(resp.Results) != 0 {
		t.Fatalf("deleted insight returned: %+v, %v", resp, err)
	}
}

func TestKeywordScoreCacheMatchesIndependentScoring(t *testing.T) {
	insights := []*model.Insight{
		{ID: "content", Content: "查询 SQLite", Tags: []string{"性能"}, Entities: []string{"ProjectX"}},
		{ID: "boundaries", Content: "查", Tags: []string{"询"}, Entities: []string{"SQLite"}},
		{ID: "none", Content: "unrelated"},
	}
	for _, query := range []string{"SQLite 查询 性能 ProjectX", "the and", "", "查询"} {
		cache := map[string]float64{}
		keywordSearchCached(insights, query, 1, cache)
		q := Tokenize(query)
		for _, ins := range insights {
			// Independent union of individually tokenized fields: no cross-field
			// CJK bigrams and no loss of tag/entity matches below the top-K cutoff.
			union := Tokenize(ins.Content)
			for _, field := range append(append([]string{}, ins.Tags...), ins.Entities...) {
				for tok := range Tokenize(field) {
					union[tok] = true
				}
			}
			var matched float64
			for tok := range q {
				if union[tok] {
					matched++
				}
			}
			want := 0.0
			if len(q) > 0 {
				want = matched / float64(len(q))
			}
			if cache[ins.ID] != want {
				t.Fatalf("query=%q id=%s: score=%v want=%v", query, ins.ID, cache[ins.ID], want)
			}
		}
	}
}

func TestRecallTimeAnchorsRespectOffsetsAndNewEdges(t *testing.T) {
	db := testDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= anchorTopK; i++ {
		created := base.Add(time.Duration(i) * time.Minute)
		if i%2 == 1 {
			created = created.In(time.FixedZone("+14", 14*60*60))
		}
		id := fmt.Sprintf("time-%02d", i)
		insertInsight(t, db, id, "time record", "test", 3, nil, created)
	}
	resp, err := IntentAwareRecall(db, "", nil, nil, 0, nil)
	if err != nil || len(resp.Results) != anchorTopK || resp.Results[0].Insight.ID != "time-20" {
		t.Fatalf("absolute-time anchors: %+v, %v", resp, err)
	}
	for _, r := range resp.Results {
		if r.Insight.ID == "time-00" {
			t.Fatal("oldest insight selected as a time anchor")
		}
	}
	if err := db.InsertEdge(&model.Edge{
		SourceID: "time-20", TargetID: "time-00", EdgeType: model.EdgeCausal, Weight: 0.5, CreatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	resp, err = IntentAwareRecall(db, "", nil, nil, 0, nil)
	if err != nil || len(resp.Results) != anchorTopK+1 {
		t.Fatalf("next recall failed to discover newly connected node: %+v, %v", resp, err)
	}
}

// The CLI now passes an already-loaded snapshot to recall. An out-of-order
// snapshot must retain time-anchor selection without mutating the caller's slice.
func TestRecallFromSnapshotPreservesTimeAnchorsAndInput(t *testing.T) {
	db := testDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < anchorTopK+3; i++ {
		insertInsight(t, db, fmt.Sprintf("snapshot-%02d", i), "record", "test", 3, nil, base.Add(time.Duration(i)*time.Minute))
	}
	all, err := db.GetAllActiveInsights()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(43))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	original := append([]*model.Insight(nil), all...)
	want, err := IntentAwareRecall(db, "", nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := IntentAwareRecallFrom(db, all, "", nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("caller snapshot changed recall: got=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(all, original) {
		t.Fatal("recall reordered the caller's snapshot")
	}
}
