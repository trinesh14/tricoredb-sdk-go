package tricoredb

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Document is one JSON document as the server stores it. Every stored document
// carries an "_id" string, whether the caller supplied it or the server
// generated one.
type Document map[string]any

// -- filters ---------------------------------------------------------------

// DocumentFilter is a document query predicate — the Go form of the server's
// DocumentFilter (crates/tricore_core/src/request/document_ops.rs).
//
// Build one with the Filter* constructors. Every field path accepts dot
// notation ("a.b.c") resolving into nested objects; a missing path never
// matches, including for FilterNe.
//
// The zero DocumentFilter is invalid, not "match everything": a
// forgotten-to-initialize filter that silently matched the whole collection
// would turn a typo into a full-collection update. Use FilterAll to say that
// on purpose.
//
// This is not MongoDB's query language: there is no Or, no Not, and no regex,
// because the server implements none of them.
type DocumentFilter struct {
	body any
}

// FilterAll matches every document.
func FilterAll() DocumentFilter { return DocumentFilter{body: "All"} }

func fieldFilter(variant, field string, value any) DocumentFilter {
	return DocumentFilter{body: map[string]any{
		variant: map[string]any{"field": field, "value": value},
	}}
}

// FilterEq matches documents whose field equals value.
func FilterEq(field string, value any) DocumentFilter { return fieldFilter("Eq", field, value) }

// FilterNe matches documents whose field exists and does not equal value.
// A document missing the field does not match.
func FilterNe(field string, value any) DocumentFilter { return fieldFilter("Ne", field, value) }

// FilterGt matches documents whose field is greater than value.
func FilterGt(field string, value any) DocumentFilter { return fieldFilter("Gt", field, value) }

// FilterGte matches documents whose field is greater than or equal to value.
func FilterGte(field string, value any) DocumentFilter { return fieldFilter("Gte", field, value) }

// FilterLt matches documents whose field is less than value.
func FilterLt(field string, value any) DocumentFilter { return fieldFilter("Lt", field, value) }

// FilterLte matches documents whose field is less than or equal to value.
func FilterLte(field string, value any) DocumentFilter { return fieldFilter("Lte", field, value) }

// FilterContains matches a string field containing value as a substring, or an
// array field containing an element equal to value.
func FilterContains(field string, value any) DocumentFilter {
	return fieldFilter("Contains", field, value)
}

// FilterIn matches documents whose field equals any of values.
func FilterIn(field string, values ...any) DocumentFilter {
	if values == nil {
		values = []any{}
	}
	return DocumentFilter{body: map[string]any{
		"In": map[string]any{"field": field, "values": values},
	}}
}

// FilterAnd matches documents satisfying every sub-filter. With no arguments it
// matches everything, which is what the server does with an empty And.
func FilterAnd(filters ...DocumentFilter) DocumentFilter {
	if filters == nil {
		filters = []DocumentFilter{}
	}
	return DocumentFilter{body: map[string]any{"And": filters}}
}

var errZeroFilter = errors.New(
	"tricoredb: uninitialized DocumentFilter — use FilterAll() to match every document")

// MarshalJSON emits the externally-tagged form the server deserializes.
func (f DocumentFilter) MarshalJSON() ([]byte, error) {
	if f.body == nil {
		return nil, errZeroFilter
	}
	return json.Marshal(f.body)
}

func (f DocumentFilter) check() error {
	if f.body == nil {
		return errZeroFilter
	}
	return nil
}

// -- updates ---------------------------------------------------------------

// DocumentUpdate is a set of field mutations applied to one document: Set
// overwrites the value at a dot path, Inc adds a number to it. Every Set is
// applied before every Inc.
//
// Inc never coerces. Incrementing a field holding a string, bool, null, array,
// or object is a server error, not a silent conversion; a missing field
// increments from 0. A negative Inc is how you decrement — there is no Dec.
type DocumentUpdate struct {
	// Set maps a dot path to its new value.
	Set map[string]any `json:"set,omitempty"`
	// Inc maps a dot path to a numeric delta. Every value must be a number.
	Inc map[string]any `json:"inc,omitempty"`
}

// -- aggregation -----------------------------------------------------------

// AggregateStage is one stage of an aggregation pipeline. Build one with the
// Stage* constructors.
//
// Stages apply strictly in the order given — the order is semantics, not
// style: $match before $group filters documents, after it filters groups.
//
// The zero AggregateStage is invalid and is refused rather than skipped: a
// pipeline that quietly dropped a stage would return a confident answer to a
// question nobody asked.
type AggregateStage struct {
	body any
}

var errZeroStage = errors.New(
	"tricoredb: uninitialized AggregateStage — use StageMatch, StageGroup, StageSort, ...")

// MarshalJSON emits the externally-tagged form the server deserializes.
func (s AggregateStage) MarshalJSON() ([]byte, error) {
	if s.body == nil {
		return nil, errZeroStage
	}
	return json.Marshal(s.body)
}

// GroupKey is how StageGroup derives a group key from a document.
type GroupKey struct {
	body any
}

// GroupByField groups by the value at a dot path. A document missing that path
// groups under null rather than being dropped.
func GroupByField(path string) GroupKey {
	return GroupKey{body: map[string]any{"Field": path}}
}

// GroupByConstant puts the whole collection in one group keyed by this
// constant. This is how a collection-wide total is expressed; the server has no
// implicit "no key".
func GroupByConstant(value any) GroupKey {
	return GroupKey{body: map[string]any{"Constant": value}}
}

// MarshalJSON emits the externally-tagged form the server deserializes.
func (k GroupKey) MarshalJSON() ([]byte, error) {
	if k.body == nil {
		return nil, errors.New("tricoredb: uninitialized GroupKey — use GroupByField or GroupByConstant")
	}
	return json.Marshal(k.body)
}

// AccumulatorOp is one reduction inside a StageGroup.
type AccumulatorOp struct {
	body any
}

// AccSum totals the numeric values at field. Documents where it is missing or
// non-numeric are ignored, so an absent field never contributes a zero.
func AccSum(field string) AccumulatorOp {
	return AccumulatorOp{body: map[string]any{"Sum": field}}
}

// AccAvg averages the numeric values at field, ignoring missing/non-numeric.
func AccAvg(field string) AccumulatorOp {
	return AccumulatorOp{body: map[string]any{"Avg": field}}
}

// AccMin is the smallest value at field.
func AccMin(field string) AccumulatorOp {
	return AccumulatorOp{body: map[string]any{"Min": field}}
}

// AccMax is the largest value at field.
func AccMax(field string) AccumulatorOp {
	return AccumulatorOp{body: map[string]any{"Max": field}}
}

// AccCount counts documents. It takes no field because it counts documents,
// not values.
func AccCount() AccumulatorOp { return AccumulatorOp{body: "Count"} }

// MarshalJSON emits the externally-tagged form the server deserializes.
func (a AccumulatorOp) MarshalJSON() ([]byte, error) {
	if a.body == nil {
		return nil, errors.New("tricoredb: uninitialized AccumulatorOp — use AccSum, AccCount, ...")
	}
	return json.Marshal(a.body)
}

// GroupAccumulator is an accumulator plus the field name it writes into the
// grouped document. Output must not be "_id" (that holds the group key) and
// must not repeat within one StageGroup.
type GroupAccumulator struct {
	// Output is the field the accumulated value is written to.
	Output string `json:"output"`
	// Op is the accumulator, built with AccSum, AccCount and friends.
	Op AccumulatorOp `json:"op"`
}

// SortKey is one StageSort key. After a StageGroup the addressable fields are
// "_id" and the accumulator outputs, not the original document's fields.
type SortKey struct {
	// Field is the field to sort by.
	Field string `json:"field"`
	// Descending sorts largest first when true.
	Descending bool `json:"descending"`
}

// StageMatch filters with the same matcher DocumentFind uses.
func StageMatch(filter DocumentFilter) AggregateStage {
	return AggregateStage{body: map[string]any{"Match": filter}}
}

// StageGroup groups by key and applies the accumulators.
func StageGroup(by GroupKey, accumulators ...GroupAccumulator) AggregateStage {
	if accumulators == nil {
		accumulators = []GroupAccumulator{}
	}
	return AggregateStage{body: map[string]any{
		"Group": map[string]any{"by": by, "accumulators": accumulators},
	}}
}

// StageSort orders the stream by the given keys.
func StageSort(keys ...SortKey) AggregateStage {
	if keys == nil {
		keys = []SortKey{}
	}
	return AggregateStage{body: map[string]any{"Sort": keys}}
}

// StageSkip drops the first n documents.
func StageSkip(n int) AggregateStage {
	return AggregateStage{body: map[string]any{"Skip": n}}
}

// StageLimit keeps at most n documents.
func StageLimit(n int) AggregateStage {
	return AggregateStage{body: map[string]any{"Limit": n}}
}

// StageProject keeps (include true) or drops (include false) the named
// top-level fields. Nested projection is not implemented server-side and is
// refused.
func StageProject(fields []string, include bool) AggregateStage {
	if fields == nil {
		fields = []string{}
	}
	return AggregateStage{body: map[string]any{
		"Project": map[string]any{"fields": fields, "include": include},
	}}
}

// StageCount replaces the stream with a single document holding the input
// count under field.
func StageCount(field string) AggregateStage {
	return AggregateStage{body: map[string]any{"Count": map[string]any{"field": field}}}
}

// -- results ---------------------------------------------------------------

// DocumentIndex describes one secondary index on a collection.
type DocumentIndex struct {
	// Name is the index name.
	Name string `json:"index_name"`
	// Field is the indexed field.
	Field string `json:"field"`
	// Unique reports whether the index enforces uniqueness.
	Unique bool `json:"unique"`
}

// DocumentStats is what DocumentAnalyze collected.
type DocumentStats struct {
	// Collection is the analyzed collection.
	Collection string `json:"analyzed"`
	// DocumentCount is the number of documents in it.
	DocumentCount int64 `json:"document_count"`
	// IndexedFields is the number of fields with a secondary index.
	IndexedFields int `json:"indexed_fields"`
}

// -- helpers ---------------------------------------------------------------

func documentOp(body any) map[string]any {
	return map[string]any{"Document": body}
}

// decodeJSON requires the response to carry the Json data variant and decodes
// its payload into out. A null payload is reported rather than left as a zero
// value, so a caller never mistakes "the server said nothing" for a real
// result.
func decodeJSON(resp *Response, what string, out any) error {
	if resp.DataKind != "Json" {
		return &ProtocolError{Message: fmt.Sprintf("expected Json for %s, got %s", what, kindOrRaw(resp))}
	}
	if isNullPayload(resp.DataRaw) {
		return &ProtocolError{Message: "null " + what + " payload"}
	}
	if err := json.Unmarshal(resp.DataRaw, out); err != nil {
		return &ProtocolError{Message: fmt.Sprintf("malformed %s payload: %v", what, err)}
	}
	return nil
}

// decodeInto unmarshals a raw payload, used where the shape is a bare scalar
// rather than the object decodeJSON expects.
func decodeInto(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}

func isNullPayload(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// decodeDocuments requires the Documents data variant and decodes it.
func decodeDocuments(resp *Response) ([]Document, error) {
	if resp.DataKind != "Documents" {
		return nil, &ProtocolError{Message: fmt.Sprintf("expected Documents, got %s", kindOrRaw(resp))}
	}
	var docs []Document
	if isNullPayload(resp.DataRaw) {
		return nil, &ProtocolError{Message: "null Documents payload"}
	}
	if err := json.Unmarshal(resp.DataRaw, &docs); err != nil {
		return nil, &ProtocolError{Message: "malformed Documents payload: " + err.Error()}
	}
	return docs, nil
}

// -- collections -----------------------------------------------------------

// DocumentCreateCollection creates a document collection.
func (c *Client) DocumentCreateCollection(collection string) error {
	_, err := c.request(documentOp(map[string]any{
		"CreateCollection": map[string]any{"collection": collection},
	}))
	return err
}

// DocumentDropCollection removes a collection, its documents, and its indexes.
// Dropping a collection that does not exist is an error.
func (c *Client) DocumentDropCollection(collection string) error {
	_, err := c.request(documentOp(map[string]any{
		"DropCollection": map[string]any{"collection": collection},
	}))
	return err
}

// DocumentListCollections names every document collection in the database.
func (c *Client) DocumentListCollections() ([]string, error) {
	resp, err := c.request(documentOp("ListCollections"))
	if err != nil {
		return nil, err
	}
	var out struct {
		Collections []string `json:"collections"`
	}
	if err := decodeJSON(resp, "ListCollections", &out); err != nil {
		return nil, err
	}
	return out.Collections, nil
}

// -- documents -------------------------------------------------------------

type insertResult struct {
	ID string `json:"id"`
}

// DocumentInsert stores document under a server-generated id, which it
// returns. document must marshal to a JSON object.
//
// Use DocumentInsertWithID to choose the id yourself.
func (c *Client) DocumentInsert(collection string, document any) (string, error) {
	return c.insert(collection, nil, document)
}

// DocumentInsertWithID stores document under the given id. Inserting over an
// existing id is an error, not an overwrite — use DocumentUpdate or
// DocumentUpsertOne for that.
func (c *Client) DocumentInsertWithID(collection, id string, document any) (string, error) {
	return c.insert(collection, id, document)
}

func (c *Client) insert(collection string, id any, document any) (string, error) {
	resp, err := c.request(documentOp(map[string]any{
		"Insert": map[string]any{"collection": collection, "id": id, "document": document},
	}))
	if err != nil {
		return "", err
	}
	var out insertResult
	if err := decodeJSON(resp, "Insert", &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", &ProtocolError{Message: "Insert returned no document id"}
	}
	return out.ID, nil
}

// DocumentGet fetches one document by id. found is false when no such document
// exists, which is how an absent document is told apart from a stored empty
// one — both otherwise look like an empty map.
func (c *Client) DocumentGet(collection, id string) (doc Document, found bool, err error) {
	resp, err := c.request(documentOp(map[string]any{
		"Get": map[string]any{"collection": collection, "id": id},
	}))
	if err != nil {
		return nil, false, err
	}
	docs, err := decodeDocuments(resp)
	if err != nil {
		return nil, false, err
	}
	if len(docs) == 0 {
		return nil, false, nil
	}
	return docs[0], true, nil
}

// DocumentFind returns every document matching filter. Use DocumentFindLimit to
// cap the result.
func (c *Client) DocumentFind(collection string, filter DocumentFilter) ([]Document, error) {
	return c.find(collection, filter, nil)
}

// DocumentFindLimit returns at most limit documents matching filter. A limit of
// zero or less means no limit.
func (c *Client) DocumentFindLimit(collection string, filter DocumentFilter, limit int) ([]Document, error) {
	if limit <= 0 {
		return c.find(collection, filter, nil)
	}
	return c.find(collection, filter, limit)
}

func (c *Client) find(collection string, filter DocumentFilter, limit any) ([]Document, error) {
	if err := filter.check(); err != nil {
		return nil, err
	}
	resp, err := c.request(documentOp(map[string]any{
		"Find": map[string]any{"collection": collection, "filter": filter, "limit": limit},
	}))
	if err != nil {
		return nil, err
	}
	return decodeDocuments(resp)
}

// DocumentUpdate sets fields on an existing document (read-modify-write). set
// maps dot paths to new values; a dot path creates or overwrites nested keys.
//
// This is not an upsert: a missing id is an error. "_id" cannot be set.
func (c *Client) DocumentUpdate(collection, id string, set map[string]any) error {
	_, err := c.request(documentOp(map[string]any{
		"Update": map[string]any{"collection": collection, "id": id, "set": set},
	}))
	return err
}

type updateOneResult struct {
	Updated  bool `json:"updated"`
	Inserted bool `json:"inserted"`
}

// DocumentUpdateOne applies update (Set and/or Inc) to one document by id.
// A missing id is an error; use DocumentUpsertOne to create instead.
func (c *Client) DocumentUpdateOne(collection, id string, update DocumentUpdate) error {
	_, err := c.updateOne(collection, id, update, false)
	return err
}

// DocumentUpsertOne applies update to one document by id, creating it from the
// update itself (carrying _id = id) when it does not exist. inserted reports
// which of the two happened.
//
// There is no id-less form: with no id there is nothing to match on, so
// "update or insert" would collapse to a plain insert under another name. Use
// DocumentInsert for that.
func (c *Client) DocumentUpsertOne(collection, id string, update DocumentUpdate) (inserted bool, err error) {
	out, err := c.updateOne(collection, id, update, true)
	if err != nil {
		return false, err
	}
	return out.Inserted, nil
}

func (c *Client) updateOne(collection, id string, update DocumentUpdate, upsert bool) (updateOneResult, error) {
	var out updateOneResult
	resp, err := c.request(documentOp(map[string]any{
		"UpdateOne": map[string]any{
			"collection": collection,
			"id":         id,
			"update":     update,
			"upsert":     upsert,
		},
	}))
	if err != nil {
		return out, err
	}
	if err := decodeJSON(resp, "UpdateOne", &out); err != nil {
		return out, err
	}
	return out, nil
}

// UpdateManyResult counts what a DocumentUpdateMany did. Matched counts
// documents the filter selected; Modified counts those whose contents actually
// changed, so an update that rewrote a document to the value it already held
// is matched but not modified.
type UpdateManyResult struct {
	Matched  int64 `json:"matched"`
	Modified int64 `json:"modified"`
}

// DocumentUpdateMany applies update to every document matching filter, using
// the same filter machinery as DocumentFind. Never an upsert: a filter
// matching nothing modifies nothing, and that is not an error.
func (c *Client) DocumentUpdateMany(collection string, filter DocumentFilter, update DocumentUpdate) (UpdateManyResult, error) {
	var out UpdateManyResult
	if err := filter.check(); err != nil {
		return out, err
	}
	resp, err := c.request(documentOp(map[string]any{
		"UpdateMany": map[string]any{
			"collection": collection,
			"filter":     filter,
			"update":     update,
		},
	}))
	if err != nil {
		return out, err
	}
	if err := decodeJSON(resp, "UpdateMany", &out); err != nil {
		return out, err
	}
	return out, nil
}

// DocumentDelete removes one document by id. Deleting an absent document is
// not an error.
func (c *Client) DocumentDelete(collection, id string) error {
	_, err := c.request(documentOp(map[string]any{
		"Delete": map[string]any{"collection": collection, "id": id},
	}))
	return err
}

// -- indexes ---------------------------------------------------------------

// DocumentCreateIndex builds a secondary index on a top-level field. Creating a
// unique index over a collection that already holds duplicates is refused,
// before anything is written.
func (c *Client) DocumentCreateIndex(collection, indexName, field string, unique bool) error {
	_, err := c.request(documentOp(map[string]any{
		"CreateIndex": map[string]any{
			"collection": collection,
			"index_name": indexName,
			"field":      field,
			"unique":     unique,
		},
	}))
	return err
}

// DocumentDropIndex removes a named index from a collection.
func (c *Client) DocumentDropIndex(collection, indexName string) error {
	_, err := c.request(documentOp(map[string]any{
		"DropIndex": map[string]any{"collection": collection, "index_name": indexName},
	}))
	return err
}

// DocumentListIndexes lists a collection's indexes.
func (c *Client) DocumentListIndexes(collection string) ([]DocumentIndex, error) {
	resp, err := c.request(documentOp(map[string]any{
		"ListIndexes": map[string]any{"collection": collection},
	}))
	if err != nil {
		return nil, err
	}
	var out struct {
		Indexes []DocumentIndex `json:"indexes"`
	}
	if err := decodeJSON(resp, "ListIndexes", &out); err != nil {
		return nil, err
	}
	return out.Indexes, nil
}

// DocumentAnalyze collects approximate statistics the query optimizer uses to
// choose between an index lookup and a scan.
func (c *Client) DocumentAnalyze(collection string) (*DocumentStats, error) {
	resp, err := c.request(documentOp(map[string]any{
		"Analyze": map[string]any{"collection": collection},
	}))
	if err != nil {
		return nil, err
	}
	var out DocumentStats
	if err := decodeJSON(resp, "Analyze", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -- aggregation -----------------------------------------------------------

// DocumentAggregate runs an aggregation pipeline over a collection. An empty
// pipeline returns the collection unchanged.
//
// Stages this build does not implement ($lookup, $unwind, $facet, $out,
// $addFields, computed expressions) are not representable here at all.
func (c *Client) DocumentAggregate(collection string, pipeline ...AggregateStage) ([]Document, error) {
	if pipeline == nil {
		pipeline = []AggregateStage{}
	}
	for i, s := range pipeline {
		if s.body == nil {
			return nil, fmt.Errorf("tricoredb: aggregate pipeline stage %d: %w", i, errZeroStage)
		}
	}
	resp, err := c.request(documentOp(map[string]any{
		"Aggregate": map[string]any{"collection": collection, "pipeline": pipeline},
	}))
	if err != nil {
		return nil, err
	}
	return decodeDocuments(resp)
}
