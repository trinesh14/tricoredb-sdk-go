package tricoredb

import (
	"encoding/json"
	"errors"
	"fmt"
)

// VectorMetric is a vector collection's distance/similarity measure. It is
// fixed when the collection is created and cannot be changed afterwards.
type VectorMetric string

const (
	// MetricCosine is cosine similarity: higher is closer.
	MetricCosine VectorMetric = "cosine"
	// MetricDot is the dot product: higher is closer.
	MetricDot VectorMetric = "dot"
	// MetricL2 is squared Euclidean distance. Note the sign: the server
	// returns it *negated*, so an l2 VectorHit.Score is <= 0 and -0.02 is
	// nearer than -196.0. See VectorHit.Score.
	MetricL2 VectorMetric = "l2"
)

// VectorQuantization selects how a collection's ANN index stores vectors in
// memory. It is an index-level choice only: the durable records always keep
// full float32 precision, so VectorGet and VectorListVectors return
// byte-identical values either way.
type VectorQuantization string

const (
	// QuantizationNone keeps full float32 vectors in the index. The default.
	QuantizationNone VectorQuantization = "none"
	// QuantizationInt8 stores int8-quantized vectors in the index (roughly 4x
	// smaller) and re-ranks the candidate pool against the durable float32
	// vectors, at the cost of one point read per candidate.
	QuantizationInt8 VectorQuantization = "int8"
)

// VectorItem is one stored vector with its metadata.
type VectorItem struct {
	ID       string         `json:"id"`
	Vector   []float32      `json:"vector"`
	Metadata map[string]any `json:"metadata"`
}

// VectorHit is one search result.
//
// Score is a similarity, never a distance: **higher is closer under every
// metric**, and Results is ordered best-first. MetricL2 is the case worth
// stating outright — the server negates the squared Euclidean distance
// (crates/tricore_vector/src/search/mod.rs) so the ranking stays uniform, which
// means an l2 Score is <= 0 and -0.02 is nearer than -196.0. Sorting these
// ascending, or reading Score as a distance, inverts an l2 ranking while a
// "is the expected id in the top-k" check still passes.
type VectorHit struct {
	ID       string         `json:"id"`
	Score    float32        `json:"score"`
	Metadata map[string]any `json:"metadata"`
	// Vector is the vector itself, when the search asked for vectors.
	Vector []float32 `json:"vector,omitempty"`
	// Reason says why the hit matched, when the search asked for an
	// explanation.
	Reason string `json:"reason,omitempty"`
}

// VectorSearchResult is a search's hits plus which path answered it: "flat" for
// the exact scan a small collection gets, "hnsw" or "hnsw-int8" for the ANN
// index.
type VectorSearchResult struct {
	Results []VectorHit `json:"results"`
	Index   string      `json:"index"`
}

// VectorCollectionSummary is one entry of VectorListCollections.
type VectorCollectionSummary struct {
	// Name is the collection name.
	Name string `json:"name"`
	// Dimension is the length every vector in the collection must have.
	Dimension int `json:"dimension"`
	// Metric is the distance metric searches rank by.
	Metric VectorMetric `json:"metric"`
}

// VectorCollectionInfo is a collection's catalog metadata plus its live vector
// count, from VectorDescribeCollection.
type VectorCollectionInfo struct {
	// Collection is the collection name.
	Collection string `json:"collection"`
	// Dimension is the length every vector in the collection must have.
	Dimension int `json:"dimension"`
	// Metric is the distance metric searches rank by.
	Metric VectorMetric `json:"metric"`
	// Count is the number of vectors currently stored.
	Count int64 `json:"count"`
	// Quantization is the index quantization the collection was created with.
	Quantization VectorQuantization `json:"quantization"`
}

// VectorPage is one page of VectorListVectors. Truncated reports that items
// remain past this page; Total is the collection's full count.
type VectorPage struct {
	// Collection is the collection name.
	Collection string `json:"collection"`
	// Count is the number of vectors on this page.
	Count int `json:"count"`
	// Vectors are this page's items.
	Vectors []VectorItem `json:"vectors"`
	// Truncated reports that more vectors remain past this page.
	Truncated bool `json:"truncated"`
	// Total is the collection's full vector count.
	Total int64 `json:"total"`
}

func vectorOp(body any) map[string]any {
	return map[string]any{"Vector": body}
}

// -- collections -----------------------------------------------------------

// VectorCreateCollection creates an unquantized collection of the given
// dimension and metric. Use VectorCreateCollectionQuantized to opt into index
// compression.
func (c *Client) VectorCreateCollection(collection string, dimension int, metric VectorMetric) error {
	return c.VectorCreateCollectionQuantized(collection, dimension, metric, QuantizationNone)
}

// VectorCreateCollectionQuantized creates a collection with an explicit index
// quantization. Dimension and metric are fixed for the collection's lifetime.
func (c *Client) VectorCreateCollectionQuantized(collection string, dimension int, metric VectorMetric, quantization VectorQuantization) error {
	if quantization == "" {
		quantization = QuantizationNone
	}
	_, err := c.request(vectorOp(map[string]any{
		"CreateCollection": map[string]any{
			"collection":   collection,
			"dimension":    dimension,
			"metric":       metric,
			"quantization": quantization,
		},
	}))
	return err
}

// VectorDropCollection removes a collection and every vector in it. Dropping a
// collection that does not exist is an error.
func (c *Client) VectorDropCollection(collection string) error {
	_, err := c.request(vectorOp(map[string]any{
		"DropCollection": map[string]any{"collection": collection},
	}))
	return err
}

// VectorListCollections describes every vector collection in the database.
func (c *Client) VectorListCollections() ([]VectorCollectionSummary, error) {
	resp, err := c.request(vectorOp("ListCollections"))
	if err != nil {
		return nil, err
	}
	var out struct {
		Details []VectorCollectionSummary `json:"details"`
	}
	if err := decodeJSON(resp, "ListCollections", &out); err != nil {
		return nil, err
	}
	return out.Details, nil
}

// VectorDescribeCollection reads one collection's catalog metadata and its live
// vector count.
func (c *Client) VectorDescribeCollection(collection string) (*VectorCollectionInfo, error) {
	resp, err := c.request(vectorOp(map[string]any{
		"DescribeCollection": map[string]any{"collection": collection},
	}))
	if err != nil {
		return nil, err
	}
	var out VectorCollectionInfo
	if err := decodeJSON(resp, "DescribeCollection", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -- vectors ---------------------------------------------------------------

// VectorUpsert stores vector under id, replacing any vector already there.
// len(vector) must equal the collection's dimension; a mismatch is refused
// rather than padded or truncated. metadata may be nil.
func (c *Client) VectorUpsert(collection, id string, vector []float32, metadata map[string]any) error {
	if vector == nil {
		vector = []float32{}
	}
	_, err := c.request(vectorOp(map[string]any{
		"Upsert": map[string]any{
			"collection": collection,
			"id":         id,
			"vector":     vector,
			"metadata":   metadata,
		},
	}))
	return err
}

// VectorGet fetches one stored vector by id. found is false when no such vector
// exists — the server answers a miss with a null payload, which would otherwise
// decode into a zero-valued VectorItem indistinguishable from a real one.
func (c *Client) VectorGet(collection, id string) (item *VectorItem, found bool, err error) {
	resp, err := c.request(vectorOp(map[string]any{
		"Get": map[string]any{"collection": collection, "id": id},
	}))
	if err != nil {
		return nil, false, err
	}
	if resp.DataKind != "Json" {
		return nil, false, &ProtocolError{Message: fmt.Sprintf("expected Json for Get, got %s", kindOrRaw(resp))}
	}
	if isNullPayload(resp.DataRaw) {
		return nil, false, nil
	}
	var out VectorItem
	if err := decodeJSON(resp, "Get", &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// VectorDelete removes one vector by id. Deleting an absent id is not an error;
// a missing collection is.
func (c *Client) VectorDelete(collection, id string) error {
	_, err := c.request(vectorOp(map[string]any{
		"Delete": map[string]any{"collection": collection, "id": id},
	}))
	return err
}

// VectorSearch returns the topK nearest vectors to the query vector. topK must
// be in 1..=1000 and len(vector) must equal the collection's dimension.
func (c *Client) VectorSearch(collection string, vector []float32, topK int) (*VectorSearchResult, error) {
	return c.VectorSearchFiltered(collection, vector, topK, nil)
}

// VectorSearchFiltered is VectorSearch restricted to vectors whose metadata
// matches filter — a map of top-level metadata field to required equal value.
// A nil or empty filter means no filtering.
//
// Exact equality only: the server implements no ranges and no nesting here.
func (c *Client) VectorSearchFiltered(collection string, vector []float32, topK int, filter map[string]any) (*VectorSearchResult, error) {
	if vector == nil {
		vector = []float32{}
	}
	op := map[string]any{
		"collection": collection,
		"vector":     vector,
		"top_k":      topK,
	}
	// An empty (but non-nil) map would be sent as `{}`, which the server reads
	// as "filter on nothing" — same as no filter, but say it plainly.
	if len(filter) > 0 {
		op["filter"] = filter
	} else {
		op["filter"] = nil
	}
	resp, err := c.request(vectorOp(map[string]any{"Search": op}))
	if err != nil {
		return nil, err
	}
	var out VectorSearchResult
	if err := decodeJSON(resp, "Search", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorListVectors reads one page of a collection's vectors, ordered by id,
// for browsing and export. limit <= 0 asks for the server default (500,
// capped at 1000); offset skips that many items.
func (c *Client) VectorListVectors(collection string, limit, offset int) (*VectorPage, error) {
	op := map[string]any{"collection": collection}
	if limit > 0 {
		op["limit"] = limit
	}
	if offset > 0 {
		op["offset"] = offset
	}
	resp, err := c.request(vectorOp(map[string]any{"ListVectors": op}))
	if err != nil {
		return nil, err
	}
	var out VectorPage
	if err := decodeJSON(resp, "ListVectors", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -- added for server 0.1.0-rc.3 ---------------------------------------------
//
// Everything below needs tricore-server 0.1.0-rc.3 or later. An older server
// does not know these operations and answers each with an error.

// ScoreMetric is the measure a search scores by, when it should differ from
// the collection's own metric.
type ScoreMetric string

const (
	ScoreCosine ScoreMetric = "cosine"
	ScoreDot    ScoreMetric = "dot"
	ScoreL2     ScoreMetric = "l2"
	ScoreL1     ScoreMetric = "l1"
	ScoreLinf   ScoreMetric = "linf"
)

// FilterStrategy is how a filtered search combines the filter with the index.
// Empty lets the server choose.
type FilterStrategy string

const (
	FilterStrategyAuto FilterStrategy = "auto"
	// FilterStrategyGraph walks the index and tests each candidate.
	FilterStrategyGraph FilterStrategy = "graph"
	// FilterStrategyExact scans the vectors the filter admits.
	FilterStrategyExact FilterStrategy = "exact"
)

// AnnIndex is which approximate index a search uses.
type AnnIndex string

const (
	AnnHnsw  AnnIndex = "hnsw"
	AnnIvf   AnnIndex = "ivf"
	AnnIvfPq AnnIndex = "ivf_pq"
)

// FusionMethod is how VectorSearchMulti merges its queries' rankings. Empty
// leaves the server's default, reciprocal rank fusion.
type FusionMethod string

const (
	// FusionRRF is reciprocal rank fusion.
	FusionRRF FusionMethod = "rrf"
	// FusionMax takes each vector's best score across the queries.
	FusionMax FusionMethod = "max"
	// FusionMean takes each vector's mean score across the queries.
	FusionMean FusionMethod = "mean"
)

// SparseVector is parallel lists of term indices and their weights.
type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

// VectorSearchOptions is what a batch or example search applies beyond its
// query vectors. A field left at its zero value is left off the wire, so the
// server's own default applies.
type VectorSearchOptions struct {
	// Filter keeps only vectors whose metadata matches it.
	Filter map[string]any
	// MinScore drops hits scoring below it. Nil means no floor.
	MinScore *float32
	// IncludeVectors returns each hit's vector with it.
	IncludeVectors bool
	// OmitMetadata leaves each hit's metadata out; the server includes it by
	// default.
	OmitMetadata bool
	// Offset skips this many hits from the front.
	Offset int
	// MaxDistance drops hits farther than it. Nil means no ceiling.
	MaxDistance *float32
	// EfSearch is the size of the candidate list the index walk keeps.
	EfSearch int
	// Exact scans every vector instead of using the index.
	Exact bool
	// ScoreMetric scores by this measure instead of the collection's metric.
	ScoreMetric ScoreMetric
	// FilterStrategy says how to combine the filter with the index.
	FilterStrategy FilterStrategy
	// Using searches this named vector instead of the collection's own.
	Using string
	// Tenant and Namespace restrict the search to one partition.
	Tenant    string
	Namespace string
	// Sparse is a sparse query to combine with the dense one.
	Sparse *SparseVector
	// HybridAlpha is the dense share of a hybrid score, from 0 to 1.
	HybridAlpha *float32
	// Explain attaches to each hit the reason it matched.
	Explain bool
	// ScoreExpr is an expression that rescores each hit.
	ScoreExpr string
	// ExplainText is the query text an explanation should refer to.
	ExplainText string
	// Ann chooses the approximate index.
	Ann AnnIndex
	// Nprobe is how many inverted-file cells to probe.
	Nprobe int
}

func (o VectorSearchOptions) write(body map[string]any) {
	if len(o.Filter) > 0 {
		body["filter"] = o.Filter
	}
	if o.MinScore != nil {
		body["min_score"] = *o.MinScore
	}
	if o.IncludeVectors {
		body["include_vectors"] = true
	}
	if o.OmitMetadata {
		body["include_metadata"] = false
	}
	putOptional(body, "offset", o.Offset)
	if o.MaxDistance != nil {
		body["max_distance"] = *o.MaxDistance
	}
	putOptional(body, "ef_search", o.EfSearch)
	if o.Exact {
		body["exact"] = true
	}
	if o.ScoreMetric != "" {
		body["score_metric"] = o.ScoreMetric
	}
	if o.FilterStrategy != "" {
		body["filter_strategy"] = o.FilterStrategy
	}
	putString(body, "using", o.Using)
	putString(body, "tenant", o.Tenant)
	putString(body, "namespace", o.Namespace)
	if o.Sparse != nil {
		body["sparse"] = o.Sparse
	}
	if o.HybridAlpha != nil {
		body["hybrid_alpha"] = *o.HybridAlpha
	}
	if o.Explain {
		body["explain"] = true
	}
	putString(body, "score_expr", o.ScoreExpr)
	putString(body, "explain_text", o.ExplainText)
	if o.Ann != "" {
		body["ann"] = o.Ann
	}
	putOptional(body, "nprobe", o.Nprobe)
}

func putString(op map[string]any, key, value string) {
	if value != "" {
		op[key] = value
	}
}

// VectorExample is one example for VectorSearchByExamples: a stored vector
// named by id, or a vector given outright. Build one with ExampleID or
// ExampleVector.
type VectorExample struct {
	body any
}

// ExampleID is the stored vector with this id.
func ExampleID(id string) VectorExample { return VectorExample{body: id} }

// ExampleVector is this vector.
func ExampleVector(vector []float32) VectorExample {
	if vector == nil {
		vector = []float32{}
	}
	return VectorExample{body: vector}
}

var errZeroExample = errors.New(
	"tricoredb: zero VectorExample; use ExampleID(id) or ExampleVector(v)")

// MarshalJSON writes a bare id or a bare vector, which is how the server tells
// the two apart.
func (e VectorExample) MarshalJSON() ([]byte, error) {
	if e.body == nil {
		return nil, errZeroExample
	}
	return json.Marshal(e.body)
}

// VectorUpsertItem is one vector of VectorUpsertMany.
type VectorUpsertItem struct {
	ID     string    `json:"id"`
	Vector []float32 `json:"vector"`
	// Metadata is stored alongside the vector. Nil stores none.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TextUpsert is one text of VectorUpsertText.
type TextUpsert struct {
	// ID is what the embedded vector is stored under.
	ID string `json:"id"`
	// Text is embedded, and kept in the metadata under "text".
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Embedding is what VectorEmbed produced.
type Embedding struct {
	// Embedder names the embedder. "hash-v1" is deterministic feature
	// hashing: lexical, with no model behind it.
	Embedder  string `json:"embedder"`
	Dimension int    `json:"dimension"`
	// Vectors holds one dense vector per text, in the order given.
	Vectors [][]float32 `json:"vectors"`
	// Sparse holds one sparse vector per text, in the order given.
	Sparse []SparseVector `json:"sparse"`
}

// VectorAlias is one alias and the collection it names.
type VectorAlias struct {
	Alias      string `json:"alias"`
	Collection string `json:"collection"`
}

// VectorPartition is one partition of a collection.
type VectorPartition struct {
	// Name is the partition's value of the field.
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// VectorPartitions is the partitions of a collection.
type VectorPartitions struct {
	// Field is the metadata field the collection was partitioned by.
	Field      string            `json:"field"`
	Partitions []VectorPartition `json:"partitions"`
	// Unpartitioned counts vectors with no value for the field.
	Unpartitioned int64 `json:"unpartitioned"`
}

// VectorIndexRebuild is what VectorRebuildIndex did.
type VectorIndexRebuild struct {
	Rebuilt           bool  `json:"rebuilt"`
	Vectors           int64 `json:"vectors"`
	BuildMs           int64 `json:"build_ms"`
	Persisted         bool  `json:"persisted"`
	TombstonesCleared int64 `json:"tombstones_cleared"`
}

// VectorIndexStats is the shape of a collection's search index.
//
// A collection small enough to be scanned has no graph: Index is "flat" and
// everything past FlatScanThreshold is zero.
type VectorIndexStats struct {
	Collection string `json:"collection"`
	// Index is "flat" for an exact scan and "hnsw" for the graph.
	Index  string `json:"index"`
	Stored int64  `json:"stored"`
	// FlatScanThreshold is the size below which a search scans.
	FlatScanThreshold int64 `json:"flat_scan_threshold"`
	Layers            int   `json:"layers"`
	// Nodes counts the graph's nodes, tombstones included; Live counts the
	// live ones.
	Nodes      int64 `json:"nodes"`
	Live       int64 `json:"live"`
	Tombstones int64 `json:"tombstones"`
	// Reachable counts the live nodes the entry point can reach.
	Reachable     int64  `json:"reachable"`
	Unreachable   int64  `json:"unreachable"`
	ShouldRebuild bool   `json:"should_rebuild"`
	Persisted     bool   `json:"persisted"`
	Entry         string `json:"entry"`
	// LivePerLayer counts live nodes on each layer, the bottom first.
	LivePerLayer []int64 `json:"live_per_layer"`
	Degree       struct {
		Min  int     `json:"min"`
		Mean float64 `json:"mean"`
		Max  int     `json:"max"`
	} `json:"degree"`
	Memory struct {
		VectorBytes   int64 `json:"vector_bytes"`
		OverheadBytes int64 `json:"overhead_bytes"`
	} `json:"memory"`
	Parameters struct {
		M              int `json:"m"`
		M0             int `json:"m0"`
		EfConstruction int `json:"ef_construction"`
		EfSearch       int `json:"ef_search"`
	} `json:"parameters"`
}

// RecallOptions shapes VectorMeasureRecall. Zero values leave the server's
// defaults.
type RecallOptions struct {
	// Sample is how many stored vectors to search with (default 100, capped
	// at 1000).
	Sample int
	// TopK is how long a result list to compare (default 10, capped at 100).
	TopK int
	// EfSearch is the candidate list size to measure at.
	EfSearch int
}

// VectorRecall is the recall VectorMeasureRecall measured.
type VectorRecall struct {
	Collection string `json:"collection"`
	Index      string `json:"index"`
	// Measured is false for a collection small enough to be scanned: there is
	// nothing to measure, and Recall is 1 by construction.
	Measured bool `json:"measured"`
	// Recall is the mean overlap of the index's results with an exact scan's.
	Recall float64 `json:"recall"`
	// MinRecall is the worst single query's overlap.
	MinRecall     float64 `json:"min_recall"`
	Queries       int     `json:"queries"`
	TopK          int     `json:"top_k"`
	EfSearch      int     `json:"ef_search"`
	FlatFallbacks int     `json:"flat_fallbacks"`
	// Reason says why nothing was measured, when nothing was.
	Reason string `json:"reason"`
}

// FusedHit is one hit of a fused ranking.
type FusedHit struct {
	ID string `json:"id"`
	// Score is the fused score. Higher is better, but the scale is the fusion
	// method's, not the collection metric's.
	Score float32 `json:"score"`
	// Matched is how many of the queries ranked it.
	Matched  int            `json:"matched"`
	Metadata map[string]any `json:"metadata"`
}

// FusedSearchResult is the answer to VectorSearchMulti.
type FusedSearchResult struct {
	Fusion  FusionMethod `json:"fusion"`
	Queries int          `json:"queries"`
	// Depth is how deep each query ranked before fusing.
	Depth   int        `json:"depth"`
	Results []FusedHit `json:"results"`
}

// VectorConfigChange is what VectorSetCollectionConfig changes. A nil or zero
// field leaves that setting as it is.
type VectorConfigChange struct {
	// Normalize stores every vector, and ranks every query, at unit length.
	// It can only change on an empty collection.
	Normalize *bool
	// M and EfConstruction shape the HNSW graph and take effect at the next
	// build, which the call forces.
	M              int
	EfConstruction int
	// MultiTenant requires every vector to carry a string _tenant, and every
	// search to name one.
	MultiTenant *bool
}

// NamedVector is one named vector declared on a collection.
type NamedVector struct {
	// Name is what a search passes as Using.
	Name      string       `json:"name"`
	Dimension int          `json:"dimension"`
	Metric    VectorMetric `json:"metric"`
}

// VectorConfig is a collection's configuration after
// VectorSetCollectionConfig.
type VectorConfig struct {
	Collection     string        `json:"collection"`
	Normalize      bool          `json:"normalize"`
	M              int           `json:"m"`
	EfConstruction int           `json:"ef_construction"`
	MultiTenant    bool          `json:"multi_tenant"`
	Named          []NamedVector `json:"named"`
}

func vectorList(vectors [][]float32) [][]float32 {
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
		if v == nil {
			v = []float32{}
		}
		out[i] = v
	}
	return out
}

// VectorListAliases lists every alias and the collection it names.
func (c *Client) VectorListAliases() ([]VectorAlias, error) {
	var out struct {
		Aliases []VectorAlias `json:"aliases"`
	}
	return out.Aliases, c.requestJSON(vectorOp("ListAliases"), "ListAliases", &out)
}

// VectorUpsertNamed writes an item's vectors by name, each with the same
// metadata. The name "default" is the collection's own vector.
func (c *Client) VectorUpsertNamed(collection, id string, vectors map[string][]float32, metadata map[string]any) error {
	body := map[string]any{"collection": collection, "id": id, "vectors": vectors}
	if metadata != nil {
		body["metadata"] = metadata
	}
	_, err := c.request(vectorOp(map[string]any{"UpsertNamed": body}))
	return err
}

// VectorSearchBatch runs several query vectors against one collection in one
// round trip.
//
// It returns one result list per query, in the order asked, each what a single
// search with the same arguments returns. Not a snapshot.
func (c *Client) VectorSearchBatch(collection string, vectors [][]float32, topK int, opts VectorSearchOptions) ([][]VectorHit, error) {
	body := map[string]any{"collection": collection, "vectors": vectorList(vectors), "top_k": topK}
	opts.write(body)
	var out struct {
		Results [][]VectorHit `json:"results"`
	}
	return out.Results, c.requestJSON(vectorOp(map[string]any{"SearchBatch": body}), "SearchBatch", &out)
}

// VectorSearchMulti searches with several query vectors and returns one fused
// ranking. An empty fusion leaves the server's default (FusionRRF); a nil or
// empty filter means no filtering.
func (c *Client) VectorSearchMulti(collection string, vectors [][]float32, topK int, fusion FusionMethod, filter map[string]any) (*FusedSearchResult, error) {
	body := map[string]any{"collection": collection, "vectors": vectorList(vectors), "top_k": topK}
	if fusion != "" {
		body["fusion"] = fusion
	}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	var out FusedSearchResult
	if err := c.requestJSON(vectorOp(map[string]any{"SearchMulti": body}), "SearchMulti", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExampleSearch is the query of VectorSearchByExamples: the sum of the
// Positive examples minus the sum of the Negative ones, so king - man + woman
// is Positive [king, woman], Negative [man].
type ExampleSearch struct {
	Positive []VectorExample
	Negative []VectorExample
	// IncludeExamples keeps the examples named by id in the results. The
	// server leaves them out by default.
	IncludeExamples bool
}

// VectorSearchByExamples searches with a query built from examples.
func (c *Client) VectorSearchByExamples(collection string, query ExampleSearch, topK int, opts VectorSearchOptions) (*VectorSearchResult, error) {
	for _, list := range [][]VectorExample{query.Positive, query.Negative} {
		for _, example := range list {
			if example.body == nil {
				return nil, &ArgumentError{Message: errZeroExample.Error()}
			}
		}
	}
	body := map[string]any{"collection": collection, "top_k": topK}
	if len(query.Positive) > 0 {
		body["positive"] = query.Positive
	}
	if len(query.Negative) > 0 {
		body["negative"] = query.Negative
	}
	if query.IncludeExamples {
		body["exclude_examples"] = false
	}
	opts.write(body)
	var out VectorSearchResult
	if err := c.requestJSON(vectorOp(map[string]any{"SearchByExamples": body}), "SearchByExamples", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorCount returns how many vectors a collection holds — or, with a
// non-empty filter, how many match it. A count, not an estimate.
func (c *Client) VectorCount(collection string, filter map[string]any) (int64, error) {
	body := map[string]any{"collection": collection}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	var out struct {
		Count int64 `json:"count"`
	}
	return out.Count, c.requestJSON(vectorOp(map[string]any{"Count": body}), "Count", &out)
}

// VectorSetCollectionConfig changes a collection's configuration, and returns
// it as it now stands. A change that sets nothing is refused before it is
// sent, as the server would refuse it.
func (c *Client) VectorSetCollectionConfig(collection string, change VectorConfigChange) (*VectorConfig, error) {
	body := map[string]any{"collection": collection}
	if change.Normalize != nil {
		body["normalize"] = *change.Normalize
	}
	putOptional(body, "m", change.M)
	putOptional(body, "ef_construction", change.EfConstruction)
	if change.MultiTenant != nil {
		body["multi_tenant"] = *change.MultiTenant
	}
	if len(body) == 1 {
		return nil, &ArgumentError{
			Message: "a configuration change must set at least one of Normalize, M, EfConstruction or MultiTenant",
		}
	}
	var out VectorConfig
	if err := c.requestJSON(vectorOp(map[string]any{"SetCollectionConfig": body}), "SetCollectionConfig", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorEmbed embeds texts with the server's built-in embedder and returns the
// vectors. Nothing is stored.
func (c *Client) VectorEmbed(texts []string, dimension int) (*Embedding, error) {
	var out Embedding
	if err := c.requestJSON(vectorOp(map[string]any{"Embed": map[string]any{"texts": strs(texts), "dimension": dimension}}), "Embed", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorUpsertText embeds each text at the collection's dimension and upserts
// the vectors as one atomic batch, returning how many were written.
func (c *Client) VectorUpsertText(collection string, items []TextUpsert) (int64, error) {
	var out struct {
		Upserted int64 `json:"upserted"`
	}
	return out.Upserted, c.requestJSON(vectorOp(map[string]any{"UpsertText": map[string]any{"collection": collection, "items": items}}), "UpsertText", &out)
}

// VectorSetAlias points an alias at a collection; an empty collection removes
// it. Every operation naming the alias acts on the collection, so a rebuilt
// collection can replace the old one by moving the alias.
func (c *Client) VectorSetAlias(alias, collection string) error {
	body := map[string]any{"alias": alias}
	body["collection"] = nil
	if collection != "" {
		body["collection"] = collection
	}
	_, err := c.request(vectorOp(map[string]any{"SetAlias": body}))
	return err
}

// VectorAddNamedVector declares a named vector on a collection: a second vector
// per item, with its own dimension and metric.
func (c *Client) VectorAddNamedVector(collection, name string, dimension int, metric VectorMetric) error {
	body := map[string]any{
		"collection": collection,
		"name":       name,
		"dimension":  dimension,
		"metric":     metric,
	}
	_, err := c.request(vectorOp(map[string]any{"AddNamedVector": body}))
	return err
}

// VectorUpsertMany writes many vectors as one atomic batch, returning how many
// were written. One bad item refuses the whole batch, and so does a repeated
// id.
func (c *Client) VectorUpsertMany(collection string, items []VectorUpsertItem) (int64, error) {
	var out struct {
		Upserted int64 `json:"upserted"`
	}
	return out.Upserted, c.requestJSON(vectorOp(map[string]any{"UpsertMany": map[string]any{"collection": collection, "items": items}}), "UpsertMany", &out)
}

// VectorDeleteMany deletes many vectors by id as one atomic batch, returning
// how many of the ids were present.
func (c *Client) VectorDeleteMany(collection string, ids ...string) (int64, error) {
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	return out.Deleted, c.requestJSON(vectorOp(map[string]any{"DeleteMany": map[string]any{"collection": collection, "ids": strs(ids)}}), "DeleteMany", &out)
}

// VectorDeleteByFilter deletes every vector whose metadata matches filter, as
// one atomic batch, returning how many went. An empty filter is refused by the
// server: emptying a collection is VectorDropCollection's job.
func (c *Client) VectorDeleteByFilter(collection string, filter map[string]any) (int64, error) {
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	return out.Deleted, c.requestJSON(vectorOp(map[string]any{"DeleteByFilter": map[string]any{"collection": collection, "filter": filter}}), "DeleteByFilter", &out)
}

// VectorUpdateByFilter merges set into the metadata of every vector matching
// filter, as one atomic batch, returning how many changed. Vectors are never
// touched.
func (c *Client) VectorUpdateByFilter(collection string, filter, set map[string]any) (int64, error) {
	body := map[string]any{
		"collection": collection,
		"filter":     filter,
		"set":        set,
	}
	var out struct {
		Updated int64 `json:"updated"`
	}
	return out.Updated, c.requestJSON(vectorOp(map[string]any{"UpdateByFilter": body}), "UpdateByFilter", &out)
}

// VectorListPartitions lists the partitions of a collection: each distinct
// value of field with its vector count. field is "_namespace" or "_tenant";
// empty leaves it to the server, which uses "_namespace".
func (c *Client) VectorListPartitions(collection, field string) (*VectorPartitions, error) {
	body := map[string]any{"collection": collection}
	if field != "" {
		body["field"] = field
	}
	var out VectorPartitions
	if err := c.requestJSON(vectorOp(map[string]any{"ListPartitions": body}), "ListPartitions", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorRenameCollection renames a collection, moving every vector with it in
// one atomic batch, and returns how many moved. Refused when to already exists.
func (c *Client) VectorRenameCollection(collection, to string) (int64, error) {
	var out struct {
		Moved int64 `json:"moved"`
	}
	return out.Moved, c.requestJSON(vectorOp(map[string]any{"RenameCollection": map[string]any{"collection": collection, "to": to}}), "RenameCollection", &out)
}

// VectorCopyCollection copies a collection, configuration and every vector, to
// a new name, and returns how many vectors were copied. Refused when to exists.
func (c *Client) VectorCopyCollection(collection, to string) (int64, error) {
	var out struct {
		Copied int64 `json:"copied"`
	}
	return out.Copied, c.requestJSON(vectorOp(map[string]any{"CopyCollection": map[string]any{"collection": collection, "to": to}}), "CopyCollection", &out)
}

// VectorRebuildIndex rebuilds the collection's search index from its stored
// vectors and persists it, clearing every tombstone.
func (c *Client) VectorRebuildIndex(collection string) (*VectorIndexRebuild, error) {
	var out VectorIndexRebuild
	if err := c.requestJSON(vectorOp(map[string]any{"RebuildIndex": map[string]any{"collection": collection}}), "RebuildIndex", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorIndexStats describes the collection's search index. It loads or builds
// the index as a search would.
func (c *Client) VectorIndexStats(collection string) (*VectorIndexStats, error) {
	var out VectorIndexStats
	if err := c.requestJSON(vectorOp(map[string]any{"IndexStats": map[string]any{"collection": collection}}), "IndexStats", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VectorMeasureRecall measures the index's recall: sampled stored vectors are
// each searched through the index and by an exact scan, and the overlap is
// reported. It costs one exact scan per sample.
func (c *Client) VectorMeasureRecall(collection string, opts RecallOptions) (*VectorRecall, error) {
	body := map[string]any{"collection": collection}
	if opts.Sample > 0 {
		body["sample"] = opts.Sample
	}
	if opts.TopK > 0 {
		body["top_k"] = opts.TopK
	}
	if opts.EfSearch > 0 {
		body["ef_search"] = opts.EfSearch
	}
	var out VectorRecall
	if err := c.requestJSON(vectorOp(map[string]any{"MeasureRecall": body}), "MeasureRecall", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
