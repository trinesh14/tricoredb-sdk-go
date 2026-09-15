package e2emodels

import (
	"errors"
	"testing"

	"github.com/trinesh14/tricoredb-sdk-go"
)

// newCollection creates a uniquely-named collection and drops it afterwards.
func newCollection(t *testing.T, db *tricoredb.Client) string {
	t.Helper()
	name := uniqueName("doc")
	mustNoErr(t, "DocumentCreateCollection", db.DocumentCreateCollection(name))
	t.Cleanup(func() { _ = db.DocumentDropCollection(name) })
	return name
}

func TestDocumentCollectionLifecycle(t *testing.T) {
	db := newClient(t)
	name := uniqueName("doc")

	mustNoErr(t, "create", db.DocumentCreateCollection(name))

	names, err := db.DocumentListCollections()
	mustNoErr(t, "list", err)
	if !contains(names, name) {
		t.Fatalf("created collection %q absent from %v", name, names)
	}

	mustNoErr(t, "drop", db.DocumentDropCollection(name))

	names, err = db.DocumentListCollections()
	mustNoErr(t, "list after drop", err)
	if contains(names, name) {
		t.Fatalf("dropped collection %q still listed in %v", name, names)
	}

	// Dropping it twice is an error, not a silent success.
	if err := db.DocumentDropCollection(name); err == nil {
		t.Fatal("dropping an absent collection returned nil error")
	}
}

// TestDocumentGetIsADistinctValueWitness proves a read returns the document
// that was written and not some other one. Two documents differing in every
// field are inserted; each read must produce its own values and none of the
// other's. A stub returning an empty document, or always the first document,
// fails here.
func TestDocumentGetIsADistinctValueWitness(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	_, err := db.DocumentInsertWithID(coll, "alpha", map[string]any{"city": "Pune", "visits": 3})
	mustNoErr(t, "insert alpha", err)
	_, err = db.DocumentInsertWithID(coll, "beta", map[string]any{"city": "Delhi", "visits": 99})
	mustNoErr(t, "insert beta", err)

	alpha, found, err := db.DocumentGet(coll, "alpha")
	mustNoErr(t, "get alpha", err)
	if !found {
		t.Fatal("get alpha: found=false for a document just inserted")
	}
	if got := str(t, alpha, "city"); got != "Pune" {
		t.Errorf("alpha.city = %q, want %q", got, "Pune")
	}
	if got := str(t, alpha, "city"); got == "Delhi" {
		t.Error("alpha came back holding beta's city — the read is returning the wrong document")
	}
	if got := num(t, alpha, "visits"); got != 3 {
		t.Errorf("alpha.visits = %v, want 3", got)
	}
	if got := str(t, alpha, "_id"); got != "alpha" {
		t.Errorf("alpha._id = %q, want %q", got, "alpha")
	}

	beta, found, err := db.DocumentGet(coll, "beta")
	mustNoErr(t, "get beta", err)
	if !found {
		t.Fatal("get beta: found=false for a document just inserted")
	}
	if got := str(t, beta, "city"); got != "Delhi" {
		t.Errorf("beta.city = %q, want %q", got, "Delhi")
	}
	if got := num(t, beta, "visits"); got != 99 {
		t.Errorf("beta.visits = %v, want 99", got)
	}

	// A miss is told apart from a hit, and reports neither an error nor a
	// zero-valued document that a caller could mistake for real data.
	miss, found, err := db.DocumentGet(coll, "no-such-document")
	mustNoErr(t, "get missing", err)
	if found {
		t.Error("get of an absent id reported found=true")
	}
	if miss != nil {
		t.Errorf("get of an absent id returned a document %v, want nil", miss)
	}
}

func TestDocumentInsertGeneratesAnID(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	id, err := db.DocumentInsert(coll, map[string]any{"city": "Chennai"})
	mustNoErr(t, "insert", err)
	if id == "" {
		t.Fatal("DocumentInsert returned an empty id with a nil error")
	}

	doc, found, err := db.DocumentGet(coll, id)
	mustNoErr(t, "get generated id", err)
	if !found {
		t.Fatalf("document under generated id %q not found", id)
	}
	if got := str(t, doc, "city"); got != "Chennai" {
		t.Errorf("city = %q, want %q", got, "Chennai")
	}

	// Reusing an id is refused, not silently overwritten.
	if _, err := db.DocumentInsertWithID(coll, id, map[string]any{"city": "Kolkata"}); err == nil {
		t.Error("inserting over an existing id returned nil error")
	}
	doc, _, err = db.DocumentGet(coll, id)
	mustNoErr(t, "get after refused insert", err)
	if got := str(t, doc, "city"); got != "Chennai" {
		t.Errorf("a refused insert changed the stored document: city = %q", got)
	}
}

// TestDocumentFindFilters checks every filter the server implements against a
// fixed corpus, asserting the exact id set each one selects — so a filter that
// matched too much (or everything) fails.
func TestDocumentFindFilters(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	corpus := []struct {
		id  string
		doc map[string]any
	}{
		{"d1", map[string]any{"city": "Pune", "age": 30, "tags": []any{"go", "rust"}, "nested": map[string]any{"k": "v1"}}},
		{"d2", map[string]any{"city": "Delhi", "age": 40, "tags": []any{"rust"}, "nested": map[string]any{"k": "v2"}}},
		{"d3", map[string]any{"city": "Pune", "age": 50, "tags": []any{"python"}, "nested": map[string]any{"k": "v3"}}},
	}
	for _, c := range corpus {
		_, err := db.DocumentInsertWithID(coll, c.id, c.doc)
		mustNoErr(t, "insert "+c.id, err)
	}

	cases := []struct {
		name   string
		filter tricoredb.DocumentFilter
		want   []string
	}{
		{"All", tricoredb.FilterAll(), []string{"d1", "d2", "d3"}},
		{"Eq", tricoredb.FilterEq("city", "Pune"), []string{"d1", "d3"}},
		{"Eq nested dot path", tricoredb.FilterEq("nested.k", "v2"), []string{"d2"}},
		{"Ne", tricoredb.FilterNe("city", "Pune"), []string{"d2"}},
		{"Gt", tricoredb.FilterGt("age", 40), []string{"d3"}},
		{"Gte", tricoredb.FilterGte("age", 40), []string{"d2", "d3"}},
		{"Lt", tricoredb.FilterLt("age", 40), []string{"d1"}},
		{"Lte", tricoredb.FilterLte("age", 40), []string{"d1", "d2"}},
		{"In", tricoredb.FilterIn("city", "Delhi", "Bengaluru"), []string{"d2"}},
		{"Contains array element", tricoredb.FilterContains("tags", "rust"), []string{"d1", "d2"}},
		{"Contains substring", tricoredb.FilterContains("city", "elh"), []string{"d2"}},
		{"And", tricoredb.FilterAnd(
			tricoredb.FilterEq("city", "Pune"),
			tricoredb.FilterGt("age", 40),
		), []string{"d3"}},
		{"And matching nothing", tricoredb.FilterAnd(
			tricoredb.FilterEq("city", "Pune"),
			tricoredb.FilterEq("city", "Delhi"),
		), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := db.DocumentFind(coll, tc.filter)
			mustNoErr(t, "find", err)
			wantExactly(t, tc.name, idsOf(t, docs), tc.want...)
		})
	}

	t.Run("limit", func(t *testing.T) {
		docs, err := db.DocumentFindLimit(coll, tricoredb.FilterAll(), 2)
		mustNoErr(t, "find limit", err)
		if len(docs) != 2 {
			t.Fatalf("limit 2 returned %d documents", len(docs))
		}
	})
}

// TestDocumentZeroFilterIsRefused proves the zero DocumentFilter cannot be used
// by accident. A zero value that meant "match everything" would turn a
// forgotten initialization into a full-collection read or update.
func TestDocumentZeroFilterIsRefused(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)
	_, err := db.DocumentInsertWithID(coll, "only", map[string]any{"a": 1})
	mustNoErr(t, "insert", err)

	var zero tricoredb.DocumentFilter

	docs, err := db.DocumentFind(coll, zero)
	if err == nil {
		t.Error("DocumentFind with a zero filter returned nil error")
	}
	if docs != nil {
		t.Errorf("DocumentFind with a zero filter returned %v, want nil", docs)
	}

	res, err := db.DocumentUpdateMany(coll, zero, tricoredb.DocumentUpdate{Inc: map[string]any{"a": 1}})
	if err == nil {
		t.Error("DocumentUpdateMany with a zero filter returned nil error")
	}
	if res.Matched != 0 || res.Modified != 0 {
		t.Errorf("a refused UpdateMany reported matched=%d modified=%d, want 0/0", res.Matched, res.Modified)
	}
	// And it really did not run.
	doc, _, err := db.DocumentGet(coll, "only")
	mustNoErr(t, "get after refused update", err)
	if got := num(t, doc, "a"); got != 1 {
		t.Errorf("a refused UpdateMany still modified the document: a = %v, want 1", got)
	}
}

func TestDocumentUpdateAndUpdateOne(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	_, err := db.DocumentInsertWithID(coll, "u1", map[string]any{"city": "Pune", "visits": 3})
	mustNoErr(t, "insert", err)

	// Update sets fields, including a nested dot path.
	mustNoErr(t, "update", db.DocumentUpdate(coll, "u1", map[string]any{
		"tier":       "gold",
		"geo.region": "west",
	}))
	doc, found, err := db.DocumentGet(coll, "u1")
	mustNoErr(t, "get after update", err)
	if !found {
		t.Fatal("document vanished after update")
	}
	if got := str(t, doc, "tier"); got != "gold" {
		t.Errorf("tier = %q, want %q", got, "gold")
	}
	geo, ok := doc["geo"].(map[string]any)
	if !ok {
		t.Fatalf("geo is %T, want a nested object (dot path did not nest)", doc["geo"])
	}
	if geo["region"] != "west" {
		t.Errorf("geo.region = %v, want %q", geo["region"], "west")
	}
	if got := num(t, doc, "visits"); got != 3 {
		t.Errorf("update clobbered an untouched field: visits = %v, want 3", got)
	}

	// UpdateOne applies set and inc together; set lands before inc.
	mustNoErr(t, "update one", db.DocumentUpdateOne(coll, "u1", tricoredb.DocumentUpdate{
		Set: map[string]any{"city": "Mumbai"},
		Inc: map[string]any{"visits": 4},
	}))
	doc, _, err = db.DocumentGet(coll, "u1")
	mustNoErr(t, "get after update one", err)
	if got := str(t, doc, "city"); got != "Mumbai" {
		t.Errorf("city = %q, want %q", got, "Mumbai")
	}
	if got := num(t, doc, "visits"); got != 7 {
		t.Errorf("visits = %v, want 7 (3 + 4)", got)
	}

	// A negative inc is how you decrement.
	mustNoErr(t, "decrement", db.DocumentUpdateOne(coll, "u1", tricoredb.DocumentUpdate{
		Inc: map[string]any{"visits": -2},
	}))
	doc, _, err = db.DocumentGet(coll, "u1")
	mustNoErr(t, "get after decrement", err)
	if got := num(t, doc, "visits"); got != 5 {
		t.Errorf("visits = %v, want 5", got)
	}
}

func TestDocumentUpsertOne(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	// Without upsert, a missing id is an error and nothing is created.
	err := db.DocumentUpdateOne(coll, "ghost", tricoredb.DocumentUpdate{Set: map[string]any{"a": 1}})
	if err == nil {
		t.Error("DocumentUpdateOne on a missing id returned nil error")
	}
	if _, found, _ := db.DocumentGet(coll, "ghost"); found {
		t.Error("a failed DocumentUpdateOne created the document anyway")
	}

	inserted, err := db.DocumentUpsertOne(coll, "ghost", tricoredb.DocumentUpdate{
		Set: map[string]any{"a": 1},
		Inc: map[string]any{"n": 5},
	})
	mustNoErr(t, "upsert (create)", err)
	if !inserted {
		t.Error("first upsert reported inserted=false")
	}
	doc, found, err := db.DocumentGet(coll, "ghost")
	mustNoErr(t, "get upserted", err)
	if !found {
		t.Fatal("upserted document not found")
	}
	if got := num(t, doc, "a"); got != 1 {
		t.Errorf("a = %v, want 1", got)
	}
	if got := num(t, doc, "n"); got != 5 {
		t.Errorf("n = %v, want 5 (inc from zero)", got)
	}
	if got := str(t, doc, "_id"); got != "ghost" {
		t.Errorf("_id = %q, want %q", got, "ghost")
	}

	inserted, err = db.DocumentUpsertOne(coll, "ghost", tricoredb.DocumentUpdate{
		Inc: map[string]any{"n": 5},
	})
	mustNoErr(t, "upsert (update)", err)
	if inserted {
		t.Error("second upsert reported inserted=true for an existing document")
	}
	doc, _, err = db.DocumentGet(coll, "ghost")
	mustNoErr(t, "get after second upsert", err)
	if got := num(t, doc, "n"); got != 10 {
		t.Errorf("n = %v, want 10", got)
	}
}

func TestDocumentUpdateMany(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	for _, c := range []struct {
		id   string
		city string
	}{{"m1", "Pune"}, {"m2", "Pune"}, {"m3", "Delhi"}} {
		_, err := db.DocumentInsertWithID(coll, c.id, map[string]any{"city": c.city, "n": 1})
		mustNoErr(t, "insert "+c.id, err)
	}

	res, err := db.DocumentUpdateMany(coll,
		tricoredb.FilterEq("city", "Pune"),
		tricoredb.DocumentUpdate{Inc: map[string]any{"n": 10}})
	mustNoErr(t, "update many", err)
	if res.Matched != 2 || res.Modified != 2 {
		t.Errorf("matched=%d modified=%d, want 2/2", res.Matched, res.Modified)
	}

	// The two Pune documents moved and the Delhi one did not — the wrong
	// answer here is "every document was updated".
	for id, want := range map[string]float64{"m1": 11, "m2": 11, "m3": 1} {
		doc, found, err := db.DocumentGet(coll, id)
		mustNoErr(t, "get "+id, err)
		if !found {
			t.Fatalf("%s missing", id)
		}
		if got := num(t, doc, "n"); got != want {
			t.Errorf("%s.n = %v, want %v", id, got, want)
		}
	}

	// A filter matching nothing modifies nothing and is not an error.
	res, err = db.DocumentUpdateMany(coll,
		tricoredb.FilterEq("city", "Nowhere"),
		tricoredb.DocumentUpdate{Inc: map[string]any{"n": 1}})
	mustNoErr(t, "update many, no matches", err)
	if res.Matched != 0 || res.Modified != 0 {
		t.Errorf("matched=%d modified=%d for a filter matching nothing, want 0/0", res.Matched, res.Modified)
	}

	// An update that changes nothing is matched but not modified.
	res, err = db.DocumentUpdateMany(coll,
		tricoredb.FilterEq("city", "Delhi"),
		tricoredb.DocumentUpdate{Set: map[string]any{"city": "Delhi"}})
	mustNoErr(t, "no-op update many", err)
	if res.Matched != 1 || res.Modified != 0 {
		t.Errorf("matched=%d modified=%d for a no-op update, want 1/0", res.Matched, res.Modified)
	}
}

func TestDocumentDelete(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	_, err := db.DocumentInsertWithID(coll, "keep", map[string]any{"a": 1})
	mustNoErr(t, "insert keep", err)
	_, err = db.DocumentInsertWithID(coll, "gone", map[string]any{"a": 2})
	mustNoErr(t, "insert gone", err)

	mustNoErr(t, "delete", db.DocumentDelete(coll, "gone"))

	if _, found, _ := db.DocumentGet(coll, "gone"); found {
		t.Error("deleted document still readable")
	}
	if _, found, _ := db.DocumentGet(coll, "keep"); !found {
		t.Error("delete removed the wrong document")
	}
	// Deleting an absent document is not an error.
	mustNoErr(t, "delete absent", db.DocumentDelete(coll, "gone"))
}

func TestDocumentIndexes(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	_, err := db.DocumentInsertWithID(coll, "i1", map[string]any{"email": "a@example.com", "city": "Pune"})
	mustNoErr(t, "insert i1", err)
	_, err = db.DocumentInsertWithID(coll, "i2", map[string]any{"email": "b@example.com", "city": "Delhi"})
	mustNoErr(t, "insert i2", err)

	ixName := coll + "_email"
	mustNoErr(t, "create index", db.DocumentCreateIndex(coll, ixName, "email", true))

	indexes, err := db.DocumentListIndexes(coll)
	mustNoErr(t, "list indexes", err)
	if len(indexes) != 1 {
		t.Fatalf("expected 1 index, got %d (%v)", len(indexes), indexes)
	}
	if indexes[0].Name != ixName || indexes[0].Field != "email" || !indexes[0].Unique {
		t.Errorf("index = %+v, want {Name:%s Field:email Unique:true}", indexes[0], ixName)
	}

	// The unique constraint is enforced, and the read path still serves the
	// right document through the index.
	if _, err := db.DocumentInsertWithID(coll, "i3", map[string]any{"email": "a@example.com"}); err == nil {
		t.Error("inserting a duplicate value into a unique index returned nil error")
	}
	docs, err := db.DocumentFind(coll, tricoredb.FilterEq("email", "b@example.com"))
	mustNoErr(t, "indexed find", err)
	wantExactly(t, "indexed find", idsOf(t, docs), "i2")

	mustNoErr(t, "drop index", db.DocumentDropIndex(coll, ixName))
	indexes, err = db.DocumentListIndexes(coll)
	mustNoErr(t, "list indexes after drop", err)
	if len(indexes) != 0 {
		t.Errorf("expected no indexes after drop, got %v", indexes)
	}
	// Dropping it twice is an error.
	if err := db.DocumentDropIndex(coll, ixName); err == nil {
		t.Error("dropping an absent index returned nil error")
	}
}

func TestDocumentAnalyze(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	for _, id := range []string{"a1", "a2", "a3"} {
		_, err := db.DocumentInsertWithID(coll, id, map[string]any{"city": "Pune"})
		mustNoErr(t, "insert "+id, err)
	}
	mustNoErr(t, "create index", db.DocumentCreateIndex(coll, coll+"_city", "city", false))

	stats, err := db.DocumentAnalyze(coll)
	mustNoErr(t, "analyze", err)
	if stats == nil {
		t.Fatal("DocumentAnalyze returned nil stats with a nil error")
	}
	if stats.Collection != coll {
		t.Errorf("Collection = %q, want %q", stats.Collection, coll)
	}
	if stats.DocumentCount != 3 {
		t.Errorf("DocumentCount = %d, want 3", stats.DocumentCount)
	}
	if stats.IndexedFields != 1 {
		t.Errorf("IndexedFields = %d, want 1", stats.IndexedFields)
	}
}

func TestDocumentAggregate(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)

	sales := []map[string]any{
		{"city": "Pune", "tier": "gold", "amount": 100},
		{"city": "Pune", "tier": "gold", "amount": 50},
		{"city": "Delhi", "tier": "gold", "amount": 70},
		{"city": "Delhi", "tier": "silver", "amount": 900},
	}
	for i, s := range sales {
		_, err := db.DocumentInsertWithID(coll, string(rune('s')+rune(i)), s)
		mustNoErr(t, "insert sale", err)
	}

	t.Run("match group sort", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll,
			tricoredb.StageMatch(tricoredb.FilterEq("tier", "gold")),
			tricoredb.StageGroup(tricoredb.GroupByField("city"),
				tricoredb.GroupAccumulator{Output: "total", Op: tricoredb.AccSum("amount")},
				tricoredb.GroupAccumulator{Output: "n", Op: tricoredb.AccCount()},
			),
			tricoredb.StageSort(tricoredb.SortKey{Field: "total", Descending: true}),
		)
		mustNoErr(t, "aggregate", err)
		if len(docs) != 2 {
			t.Fatalf("expected 2 groups, got %d (%v)", len(docs), docs)
		}
		// The silver 900 must be absent: if $match were dropped, Delhi would
		// lead with 970 and this ordering assertion catches it.
		if got := str(t, docs[0], "_id"); got != "Pune" {
			t.Errorf("first group = %q, want Pune (Delhi first means $match was ignored)", got)
		}
		if got := num(t, docs[0], "total"); got != 150 {
			t.Errorf("Pune total = %v, want 150", got)
		}
		if got := num(t, docs[0], "n"); got != 2 {
			t.Errorf("Pune count = %v, want 2", got)
		}
		if got := num(t, docs[1], "total"); got != 70 {
			t.Errorf("Delhi total = %v, want 70", got)
		}
	})

	t.Run("constant group min max avg", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll,
			tricoredb.StageGroup(tricoredb.GroupByConstant("all"),
				tricoredb.GroupAccumulator{Output: "lo", Op: tricoredb.AccMin("amount")},
				tricoredb.GroupAccumulator{Output: "hi", Op: tricoredb.AccMax("amount")},
				tricoredb.GroupAccumulator{Output: "avg", Op: tricoredb.AccAvg("amount")},
			),
		)
		mustNoErr(t, "aggregate", err)
		if len(docs) != 1 {
			t.Fatalf("expected 1 group, got %d (%v)", len(docs), docs)
		}
		if got := num(t, docs[0], "lo"); got != 50 {
			t.Errorf("min = %v, want 50", got)
		}
		if got := num(t, docs[0], "hi"); got != 900 {
			t.Errorf("max = %v, want 900", got)
		}
		if got := num(t, docs[0], "avg"); got != 280 {
			t.Errorf("avg = %v, want 280", got)
		}
	})

	t.Run("count stage", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll, tricoredb.StageCount("n"))
		mustNoErr(t, "aggregate", err)
		if len(docs) != 1 || num(t, docs[0], "n") != 4 {
			t.Fatalf("count stage = %v, want a single {n:4}", docs)
		}
	})

	t.Run("skip and limit", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll,
			tricoredb.StageSort(tricoredb.SortKey{Field: "amount", Descending: false}),
			tricoredb.StageSkip(1),
			tricoredb.StageLimit(2),
		)
		mustNoErr(t, "aggregate", err)
		if len(docs) != 2 {
			t.Fatalf("expected 2 documents, got %d (%v)", len(docs), docs)
		}
		if got := num(t, docs[0], "amount"); got != 70 {
			t.Errorf("first amount = %v, want 70 (sorted 50,70,100,900 then skip 1)", got)
		}
		if got := num(t, docs[1], "amount"); got != 100 {
			t.Errorf("second amount = %v, want 100", got)
		}
	})

	t.Run("project", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll,
			tricoredb.StageMatch(tricoredb.FilterEq("tier", "silver")),
			tricoredb.StageProject([]string{"city"}, true),
		)
		mustNoErr(t, "aggregate", err)
		if len(docs) != 1 {
			t.Fatalf("expected 1 document, got %d (%v)", len(docs), docs)
		}
		if _, ok := docs[0]["amount"]; ok {
			t.Errorf("excluded field `amount` survived the projection: %v", docs[0])
		}
		if got := str(t, docs[0], "city"); got != "Delhi" {
			t.Errorf("city = %q, want Delhi", got)
		}
	})

	t.Run("empty pipeline returns the collection", func(t *testing.T) {
		docs, err := db.DocumentAggregate(coll)
		mustNoErr(t, "aggregate", err)
		if len(docs) != 4 {
			t.Errorf("empty pipeline returned %d documents, want 4", len(docs))
		}
	})

	t.Run("zero stage is refused", func(t *testing.T) {
		var zero tricoredb.AggregateStage
		docs, err := db.DocumentAggregate(coll, tricoredb.StageCount("n"), zero)
		if err == nil {
			t.Error("a zero AggregateStage returned nil error")
		}
		if docs != nil {
			t.Errorf("a refused pipeline returned %v, want nil", docs)
		}
	})
}

// TestDocumentErrorsAreTypedAndNotSilentlyZero is the nil-error trap: every
// refusal below must produce a non-nil error AND leave the returned value at
// something the caller cannot mistake for a real result.
func TestDocumentErrorsAreTypedAndNotSilentlyZero(t *testing.T) {
	db := newClient(t)
	coll := newCollection(t, db)
	_, err := db.DocumentInsertWithID(coll, "real", map[string]any{"city": "Pune"})
	mustNoErr(t, "insert", err)

	t.Run("get on a missing collection", func(t *testing.T) {
		doc, found, err := db.DocumentGet("go_no_such_collection", "x")
		if err == nil {
			t.Fatal("nil error for a get on a collection that does not exist")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if found {
			t.Error("found=true alongside an error")
		}
		if doc != nil {
			t.Errorf("returned document %v alongside an error, want nil", doc)
		}
	})

	t.Run("find on a missing collection", func(t *testing.T) {
		docs, err := db.DocumentFind("go_no_such_collection", tricoredb.FilterAll())
		if err == nil {
			t.Fatal("nil error for a find on a collection that does not exist")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if docs != nil {
			t.Errorf("returned %v alongside an error, want nil (an empty slice reads as 'no matches')", docs)
		}
	})

	t.Run("insert returns no id on failure", func(t *testing.T) {
		id, err := db.DocumentInsert("go_no_such_collection", map[string]any{"a": 1})
		if err == nil {
			t.Fatal("nil error inserting into a collection that does not exist")
		}
		if id != "" {
			t.Errorf("returned id %q alongside an error, want \"\"", id)
		}
	})

	t.Run("insert of a non-object", func(t *testing.T) {
		id, err := db.DocumentInsert(coll, []any{1, 2, 3})
		if err == nil {
			t.Fatal("nil error inserting a JSON array as a document")
		}
		if id != "" {
			t.Errorf("returned id %q alongside an error", id)
		}
	})

	t.Run("update of a missing document", func(t *testing.T) {
		err := db.DocumentUpdate(coll, "nope", map[string]any{"a": 1})
		if err == nil {
			t.Fatal("nil error updating a document that does not exist (Update is not an upsert)")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
	})

	t.Run("empty set is refused", func(t *testing.T) {
		if err := db.DocumentUpdate(coll, "real", map[string]any{}); err == nil {
			t.Error("nil error for an empty set")
		}
	})

	t.Run("inc on a non-numeric field", func(t *testing.T) {
		err := db.DocumentUpdateOne(coll, "real", tricoredb.DocumentUpdate{
			Inc: map[string]any{"city": 1},
		})
		if err == nil {
			t.Fatal("nil error incrementing a string field (no coercion is performed)")
		}
		doc, _, gerr := db.DocumentGet(coll, "real")
		mustNoErr(t, "get after refused inc", gerr)
		if got := str(t, doc, "city"); got != "Pune" {
			t.Errorf("a refused inc changed the field: city = %q", got)
		}
	})

	t.Run("analyze on a missing collection", func(t *testing.T) {
		stats, err := db.DocumentAnalyze("go_no_such_collection")
		if err == nil {
			t.Fatal("nil error analyzing a collection that does not exist")
		}
		if stats != nil {
			t.Errorf("returned stats %+v alongside an error, want nil", stats)
		}
	})

	t.Run("list indexes on a missing collection", func(t *testing.T) {
		ix, err := db.DocumentListIndexes("go_no_such_collection")
		if err == nil {
			t.Fatal("nil error listing indexes of a collection that does not exist")
		}
		if ix != nil {
			t.Errorf("returned %v alongside an error, want nil", ix)
		}
	})

	t.Run("aggregate on a missing collection", func(t *testing.T) {
		docs, err := db.DocumentAggregate("go_no_such_collection", tricoredb.StageCount("n"))
		if err == nil {
			t.Fatal("nil error aggregating a collection that does not exist")
		}
		if docs != nil {
			t.Errorf("returned %v alongside an error, want nil", docs)
		}
	})
}
