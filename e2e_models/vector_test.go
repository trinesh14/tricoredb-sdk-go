package e2emodels

import (
	"errors"
	"testing"

	"github.com/trinesh14/tricoredb-sdk-go"
)

func newVectorCollection(t *testing.T, db *tricoredb.Client, dim int, metric tricoredb.VectorMetric) string {
	t.Helper()
	name := uniqueName("vec")
	mustNoErr(t, "VectorCreateCollection", db.VectorCreateCollection(name, dim, metric))
	t.Cleanup(func() { _ = db.VectorDropCollection(name) })
	return name
}

func TestVectorCollectionLifecycle(t *testing.T) {
	db := newClient(t)
	name := uniqueName("vec")

	mustNoErr(t, "create", db.VectorCreateCollection(name, 4, tricoredb.MetricDot))

	info, err := db.VectorDescribeCollection(name)
	mustNoErr(t, "describe", err)
	if info == nil {
		t.Fatal("VectorDescribeCollection returned nil with a nil error")
	}
	if info.Collection != name {
		t.Errorf("Collection = %q, want %q", info.Collection, name)
	}
	if info.Dimension != 4 {
		t.Errorf("Dimension = %d, want 4", info.Dimension)
	}
	if info.Metric != tricoredb.MetricDot {
		t.Errorf("Metric = %q, want %q", info.Metric, tricoredb.MetricDot)
	}
	if info.Quantization != tricoredb.QuantizationNone {
		t.Errorf("Quantization = %q, want %q", info.Quantization, tricoredb.QuantizationNone)
	}
	if info.Count != 0 {
		t.Errorf("Count = %d for a new collection, want 0", info.Count)
	}

	summaries, err := db.VectorListCollections()
	mustNoErr(t, "list", err)
	var found *tricoredb.VectorCollectionSummary
	for i := range summaries {
		if summaries[i].Name == name {
			found = &summaries[i]
		}
	}
	if found == nil {
		t.Fatalf("created collection %q absent from %v", name, summaries)
	}
	if found.Dimension != 4 || found.Metric != tricoredb.MetricDot {
		t.Errorf("summary = %+v, want dimension 4 metric dot", *found)
	}

	// Creating it twice is refused.
	if err := db.VectorCreateCollection(name, 4, tricoredb.MetricDot); err == nil {
		t.Error("creating an existing collection returned nil error")
	}

	mustNoErr(t, "drop", db.VectorDropCollection(name))
	if _, err := db.VectorDescribeCollection(name); err == nil {
		t.Error("describing a dropped collection returned nil error")
	}
	if err := db.VectorDropCollection(name); err == nil {
		t.Error("dropping an absent collection returned nil error")
	}
}

// TestVectorGetIsADistinctValueWitness proves a read returns the vector that
// was written, component by component, and not some other stored vector. A
// stub returning an empty or zero-valued item fails here.
func TestVectorGetIsADistinctValueWitness(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)

	mustNoErr(t, "upsert v1", db.VectorUpsert(coll, "v1",
		[]float32{0.25, -0.5, 0.75}, map[string]any{"kind": "alpha"}))
	mustNoErr(t, "upsert v2", db.VectorUpsert(coll, "v2",
		[]float32{-1, 2, -3}, map[string]any{"kind": "beta"}))

	item, found, err := db.VectorGet(coll, "v1")
	mustNoErr(t, "get v1", err)
	if !found {
		t.Fatal("get v1: found=false for a vector just written")
	}
	if item == nil {
		t.Fatal("get v1: nil item with found=true")
	}
	if item.ID != "v1" {
		t.Errorf("ID = %q, want v1", item.ID)
	}
	want := []float32{0.25, -0.5, 0.75}
	if len(item.Vector) != len(want) {
		t.Fatalf("Vector has %d components, want %d (%v)", len(item.Vector), len(want), item.Vector)
	}
	for i := range want {
		if item.Vector[i] != want[i] {
			t.Errorf("Vector[%d] = %v, want %v (full: %v)", i, item.Vector[i], want[i], item.Vector)
		}
	}
	if item.Metadata["kind"] != "alpha" {
		t.Errorf("Metadata[kind] = %v, want alpha", item.Metadata["kind"])
	}
	if item.Metadata["kind"] == "beta" {
		t.Error("v1 came back holding v2's metadata — the read returns the wrong vector")
	}

	item2, found, err := db.VectorGet(coll, "v2")
	mustNoErr(t, "get v2", err)
	if !found || item2 == nil {
		t.Fatal("get v2 failed")
	}
	if item2.Vector[0] != -1 || item2.Vector[1] != 2 || item2.Vector[2] != -3 {
		t.Errorf("v2 vector = %v, want [-1 2 -3]", item2.Vector)
	}

	// An upsert overwrites in place.
	mustNoErr(t, "re-upsert v1", db.VectorUpsert(coll, "v1",
		[]float32{9, 9, 9}, map[string]any{"kind": "gamma"}))
	item, _, err = db.VectorGet(coll, "v1")
	mustNoErr(t, "get v1 after overwrite", err)
	if item.Vector[0] != 9 {
		t.Errorf("upsert did not overwrite: %v", item.Vector)
	}

	// A miss is neither an error nor a zero-valued item.
	miss, found, err := db.VectorGet(coll, "no-such-vector")
	mustNoErr(t, "get missing", err)
	if found {
		t.Error("get of an absent id reported found=true")
	}
	if miss != nil {
		t.Errorf("get of an absent id returned %+v, want nil", miss)
	}
}

// TestVectorSearchRanksByCloseness searches the same collection from two
// different query points and requires the rankings to differ. Membership alone
// is not a witness: a search that returned every id in a fixed order would pass
// "is the expected id in the top-k" while ranking nothing.
func TestVectorSearchRanksByCloseness(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)

	mustNoErr(t, "upsert x", db.VectorUpsert(coll, "x", []float32{1, 0, 0}, nil))
	mustNoErr(t, "upsert y", db.VectorUpsert(coll, "y", []float32{0.8, 0.6, 0}, nil))
	mustNoErr(t, "upsert z", db.VectorUpsert(coll, "z", []float32{0, 0, 1}, nil))

	// From [1,0,0]: cos(x)=1.0, cos(y)=0.8, cos(z)=0.0 -> x, y, z.
	near, err := db.VectorSearch(coll, []float32{1, 0, 0}, 3)
	mustNoErr(t, "search near x", err)
	if near == nil {
		t.Fatal("VectorSearch returned nil with a nil error")
	}
	gotX := hitIDs(near.Results)
	if len(gotX) != 3 {
		t.Fatalf("expected 3 hits, got %d (%v)", len(gotX), gotX)
	}
	if gotX[0] != "x" {
		t.Errorf("nearest to [1,0,0] is %q, want x (ranking is wrong, not just membership)", gotX[0])
	}
	if gotX[2] != "z" {
		t.Errorf("furthest from [1,0,0] is %q, want z", gotX[2])
	}

	// From [0,0.6,0.8]: cos(z)=0.8, cos(y)=0.36, cos(x)=0.0 -> z, y, x.
	far, err := db.VectorSearch(coll, []float32{0, 0.6, 0.8}, 3)
	mustNoErr(t, "search near z", err)
	gotZ := hitIDs(far.Results)
	if gotZ[0] != "z" {
		t.Errorf("nearest to [0,0.6,0.8] is %q, want z", gotZ[0])
	}
	if gotZ[2] != "x" {
		t.Errorf("furthest from [0,0.6,0.8] is %q, want x", gotZ[2])
	}

	// The two rankings must actually differ. Identical orderings would mean the
	// query vector is being ignored.
	if gotX[0] == gotZ[0] {
		t.Errorf("two different query points produced the same ranking (%v vs %v) — "+
			"the query vector is not affecting the result", gotX, gotZ)
	}

	// Scores are ordered best-first and, for cosine, decrease.
	for i := 1; i < len(near.Results); i++ {
		if near.Results[i].Score > near.Results[i-1].Score {
			t.Errorf("cosine results are not best-first: score[%d]=%v > score[%d]=%v",
				i, near.Results[i].Score, i-1, near.Results[i-1].Score)
		}
	}

	// top_k caps the result.
	one, err := db.VectorSearch(coll, []float32{1, 0, 0}, 1)
	mustNoErr(t, "search top_k=1", err)
	if len(one.Results) != 1 || one.Results[0].ID != "x" {
		t.Errorf("top_k=1 returned %v, want exactly [x]", hitIDs(one.Results))
	}
	if one.Index != "flat" {
		t.Errorf("Index = %q, want flat for a 3-vector collection", one.Index)
	}
}

// TestVectorL2ScoreIsASimilarityNotADistance pins the sign convention. The
// server negates squared L2 so that higher is closer under every metric; an SDK
// that models Score as a distance ranks every l2 search backwards, and a
// membership-only test would not notice.
func TestVectorL2ScoreIsASimilarityNotADistance(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 2, tricoredb.MetricL2)

	mustNoErr(t, "upsert near", db.VectorUpsert(coll, "near", []float32{0, 0}, nil))
	mustNoErr(t, "upsert mid", db.VectorUpsert(coll, "mid", []float32{3, 0}, nil))
	mustNoErr(t, "upsert far", db.VectorUpsert(coll, "far", []float32{14, 0}, nil))

	res, err := db.VectorSearch(coll, []float32{0.1, 0}, 3)
	mustNoErr(t, "search", err)
	got := hitIDs(res.Results)
	if len(got) != 3 {
		t.Fatalf("expected 3 hits, got %v", got)
	}

	// Ordering: nearest first, furthest last.
	if got[0] != "near" || got[2] != "far" {
		t.Errorf("l2 ranking = %v, want [near mid far]", got)
	}

	// Every l2 score is a negated squared distance, so <= 0.
	for _, h := range res.Results {
		if h.Score > 0 {
			t.Errorf("l2 score for %q is %v; the server returns a *negated* squared "+
				"distance, so it must be <= 0", h.ID, h.Score)
		}
	}

	// The decisive assertion: the nearest vector has the *greatest* score.
	// Treating Score as a distance and sorting ascending inverts this.
	nearest, furthest := res.Results[0].Score, res.Results[2].Score
	if !(nearest > furthest) {
		t.Errorf("nearest score %v is not greater than furthest score %v — "+
			"higher must be closer under every metric", nearest, furthest)
	}
	// And they are far apart, so this is not passing on a tie.
	if nearest-furthest < 1 {
		t.Errorf("scores %v and %v are suspiciously close; expected a wide spread "+
			"between distance 0.01 and distance ~193", nearest, furthest)
	}

	// Searching from the other end reverses the ranking.
	rev, err := db.VectorSearch(coll, []float32{14, 0}, 3)
	mustNoErr(t, "reverse search", err)
	revIDs := hitIDs(rev.Results)
	if revIDs[0] != "far" || revIDs[2] != "near" {
		t.Errorf("reverse l2 ranking = %v, want [far mid near]", revIDs)
	}
}

func TestVectorSearchFiltered(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)

	mustNoErr(t, "upsert a", db.VectorUpsert(coll, "a", []float32{1, 0, 0}, map[string]any{"kind": "doc", "lang": "go"}))
	mustNoErr(t, "upsert b", db.VectorUpsert(coll, "b", []float32{0.9, 0.1, 0}, map[string]any{"kind": "code", "lang": "go"}))
	mustNoErr(t, "upsert c", db.VectorUpsert(coll, "c", []float32{0.8, 0.2, 0}, map[string]any{"kind": "code", "lang": "rust"}))

	// Unfiltered, "a" wins.
	all, err := db.VectorSearch(coll, []float32{1, 0, 0}, 3)
	mustNoErr(t, "unfiltered", err)
	if hitIDs(all.Results)[0] != "a" {
		t.Fatalf("unfiltered nearest = %v, want a", hitIDs(all.Results))
	}

	// Filtered to kind=code, "a" must be genuinely absent — not merely ranked
	// lower — and "b" must lead.
	code, err := db.VectorSearchFiltered(coll, []float32{1, 0, 0}, 3, map[string]any{"kind": "code"})
	mustNoErr(t, "filter kind=code", err)
	wantExactly(t, "filter kind=code", hitIDs(code.Results), "b", "c")
	if code.Results[0].ID != "b" {
		t.Errorf("filtered nearest = %q, want b", code.Results[0].ID)
	}

	// Two conjoined fields.
	both, err := db.VectorSearchFiltered(coll, []float32{1, 0, 0}, 3,
		map[string]any{"kind": "code", "lang": "rust"})
	mustNoErr(t, "filter kind+lang", err)
	wantExactly(t, "filter kind+lang", hitIDs(both.Results), "c")

	// A filter matching nothing yields no hits and no error.
	none, err := db.VectorSearchFiltered(coll, []float32{1, 0, 0}, 3, map[string]any{"kind": "nothing"})
	mustNoErr(t, "filter matching nothing", err)
	if len(none.Results) != 0 {
		t.Errorf("filter matching nothing returned %v", hitIDs(none.Results))
	}
}

func TestVectorListVectorsPaginates(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 2, tricoredb.MetricDot)

	// Ids are returned sorted, so name them in a known order.
	for _, id := range []string{"p1", "p2", "p3"} {
		mustNoErr(t, "upsert "+id, db.VectorUpsert(coll, id, []float32{1, 2}, map[string]any{"id": id}))
	}

	page, err := db.VectorListVectors(coll, 0, 0)
	mustNoErr(t, "list all", err)
	if page == nil {
		t.Fatal("VectorListVectors returned nil with a nil error")
	}
	if page.Total != 3 || page.Count != 3 {
		t.Errorf("Total=%d Count=%d, want 3/3", page.Total, page.Count)
	}
	if page.Truncated {
		t.Error("Truncated=true for a full page")
	}
	if len(page.Vectors) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(page.Vectors))
	}

	// limit + offset select a specific item, not just "some item".
	second, err := db.VectorListVectors(coll, 1, 1)
	mustNoErr(t, "list limit 1 offset 1", err)
	if len(second.Vectors) != 1 {
		t.Fatalf("expected 1 vector, got %d (%v)", len(second.Vectors), second.Vectors)
	}
	if second.Vectors[0].ID != "p2" {
		t.Errorf("limit=1 offset=1 returned %q, want p2 (ids are ordered)", second.Vectors[0].ID)
	}
	if second.Total != 3 {
		t.Errorf("Total = %d, want 3 (the collection total, not the page size)", second.Total)
	}
	if !second.Truncated {
		t.Error("Truncated=false when a page of 1 leaves 1 item unread")
	}

	// Deleting removes exactly one.
	mustNoErr(t, "delete p2", db.VectorDelete(coll, "p2"))
	page, err = db.VectorListVectors(coll, 0, 0)
	mustNoErr(t, "list after delete", err)
	var ids []string
	for _, v := range page.Vectors {
		ids = append(ids, v.ID)
	}
	wantExactly(t, "after delete", ids, "p1", "p3")

	// Deleting an absent id is not an error.
	mustNoErr(t, "delete absent", db.VectorDelete(coll, "p2"))
}

func TestVectorQuantizedCollection(t *testing.T) {
	db := newClient(t)
	name := uniqueName("vecq")
	mustNoErr(t, "create int8", db.VectorCreateCollectionQuantized(
		name, 3, tricoredb.MetricL2, tricoredb.QuantizationInt8))
	t.Cleanup(func() { _ = db.VectorDropCollection(name) })

	info, err := db.VectorDescribeCollection(name)
	mustNoErr(t, "describe", err)
	if info.Quantization != tricoredb.QuantizationInt8 {
		t.Errorf("Quantization = %q, want int8", info.Quantization)
	}

	// Quantization is an index-level choice: the durable record keeps full
	// float32 precision, so a read must be exact.
	exact := []float32{0.125, -0.375, 0.625}
	mustNoErr(t, "upsert", db.VectorUpsert(name, "q1", exact, nil))
	item, found, err := db.VectorGet(name, "q1")
	mustNoErr(t, "get", err)
	if !found || item == nil {
		t.Fatal("quantized collection: vector not found after upsert")
	}
	for i := range exact {
		if item.Vector[i] != exact[i] {
			t.Errorf("quantization lost durable precision: Vector[%d] = %v, want %v (full: %v)",
				i, item.Vector[i], exact[i], item.Vector)
		}
	}
}

// TestVectorErrorsAreTypedAndNotSilentlyZero is the nil-error trap for the
// vector API.
func TestVectorErrorsAreTypedAndNotSilentlyZero(t *testing.T) {
	db := newClient(t)
	coll := newVectorCollection(t, db, 3, tricoredb.MetricCosine)
	mustNoErr(t, "upsert", db.VectorUpsert(coll, "v", []float32{1, 0, 0}, nil))

	t.Run("get on a missing collection", func(t *testing.T) {
		item, found, err := db.VectorGet("go_no_such_vec_collection", "v")
		if err == nil {
			t.Fatal("nil error for a get on a collection that does not exist")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if found {
			t.Error("found=true alongside an error")
		}
		if item != nil {
			t.Errorf("returned %+v alongside an error, want nil", item)
		}
	})

	t.Run("dimension mismatch on upsert", func(t *testing.T) {
		err := db.VectorUpsert(coll, "bad", []float32{1, 0}, nil)
		if err == nil {
			t.Fatal("nil error upserting a 2-component vector into a 3-dimension collection")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if _, found, _ := db.VectorGet(coll, "bad"); found {
			t.Error("a refused upsert stored the vector anyway")
		}
	})

	t.Run("dimension mismatch on search", func(t *testing.T) {
		res, err := db.VectorSearch(coll, []float32{1, 0}, 1)
		if err == nil {
			t.Fatal("nil error searching with a wrong-dimension query vector")
		}
		if res != nil {
			t.Errorf("returned %+v alongside an error, want nil (an empty result reads as 'no matches')", res)
		}
	})

	t.Run("top_k out of range", func(t *testing.T) {
		for _, k := range []int{0, 1001} {
			res, err := db.VectorSearch(coll, []float32{1, 0, 0}, k)
			if err == nil {
				t.Errorf("nil error for top_k=%d (the server accepts 1..=1000)", k)
			}
			if res != nil {
				t.Errorf("top_k=%d returned %+v alongside an error, want nil", k, res)
			}
		}
	})

	t.Run("describe a missing collection", func(t *testing.T) {
		info, err := db.VectorDescribeCollection("go_no_such_vec_collection")
		if err == nil {
			t.Fatal("nil error describing a collection that does not exist")
		}
		if info != nil {
			t.Errorf("returned %+v alongside an error, want nil", info)
		}
	})

	t.Run("list vectors of a missing collection", func(t *testing.T) {
		page, err := db.VectorListVectors("go_no_such_vec_collection", 0, 0)
		if err == nil {
			t.Fatal("nil error listing a collection that does not exist")
		}
		if page != nil {
			t.Errorf("returned %+v alongside an error, want nil", page)
		}
	})

	// A payload the server cannot deserialize is answered with a generic
	// protocol-level refusal rather than a per-field message, so this asserts
	// error identity and non-nil-ness, never message text.
	t.Run("malformed payload: an unrecognized metric", func(t *testing.T) {
		err := db.VectorCreateCollection(uniqueName("bad"), 3, tricoredb.VectorMetric("banana"))
		if err == nil {
			t.Fatal("nil error creating a collection with an unrecognized metric")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		// The connection survives a rejected request and is still usable.
		if _, _, err := db.VectorGet(coll, "v"); err != nil {
			t.Errorf("connection unusable after a malformed request: %v", err)
		}
	})

	t.Run("malformed payload: an unrecognized quantization", func(t *testing.T) {
		err := db.VectorCreateCollectionQuantized(uniqueName("bad"), 3,
			tricoredb.MetricCosine, tricoredb.VectorQuantization("float2"))
		if err == nil {
			t.Fatal("nil error creating a collection with an unrecognized quantization")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
	})
}

func hitIDs(hits []tricoredb.VectorHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}
