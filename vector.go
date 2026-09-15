package tricoredb

import "fmt"

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
