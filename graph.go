package tricoredb

import "fmt"

// GraphDirection selects which edges of a node an operation follows.
//
// The empty GraphDirection means "let the server decide", which is
// DirectionOutgoing everywhere it appears.
type GraphDirection string

const (
	// DirectionOutgoing follows edges where the node is the `from` endpoint.
	DirectionOutgoing GraphDirection = "outgoing"
	// DirectionIncoming follows edges where the node is the `to` endpoint.
	DirectionIncoming GraphDirection = "incoming"
	// DirectionBoth follows edges in either direction.
	DirectionBoth GraphDirection = "both"
)

// GraphNode is one stored node.
type GraphNode struct {
	// ID is the node id, unique within its graph.
	ID string `json:"id"`
	// Labels are the node's labels.
	Labels []string `json:"labels"`
	// Properties are the node's properties as decoded JSON.
	Properties map[string]any `json:"properties"`
}

// GraphEdge is one stored edge, directed From -> To.
type GraphEdge struct {
	// ID is the edge id, unique within its graph.
	ID string `json:"id"`
	// From is the id of the node the edge leaves.
	From string `json:"from"`
	// To is the id of the node the edge enters.
	To string `json:"to"`
	// Label is the edge's label.
	Label string `json:"label"`
	// Properties are the edge's properties as decoded JSON.
	Properties map[string]any `json:"properties"`
}

// GraphNeighbor is one incident edge of a node and the node at its other end.
// Direction is this edge's orientation relative to the node asked about, which
// is what makes a DirectionBoth result readable.
type GraphNeighbor struct {
	// EdgeID is the incident edge.
	EdgeID string `json:"edge_id"`
	// NodeID is the node at the edge's other end.
	NodeID string `json:"node_id"`
	// Label is the edge's label.
	Label string `json:"label"`
	// Direction is the edge's orientation relative to the node asked about.
	Direction GraphDirection `json:"direction"`
}

// GraphVisit is one node reached by GraphTraverse, with the hop count at which
// it was first reached.
type GraphVisit struct {
	// ID is the node id.
	ID string `json:"id"`
	// Depth is the hop count at which the node was first reached.
	Depth int `json:"depth"`
	// Labels are the node's labels.
	Labels []string `json:"labels"`
	// Properties are the node's properties as decoded JSON.
	Properties map[string]any `json:"properties"`
}

// GraphTraversal is the outcome of a bounded BFS. Truncated reports that a
// limit or the server's visited-node cap stopped the walk before it ran out of
// nodes — the nodes returned are real, the answer is just not exhaustive.
type GraphTraversal struct {
	// Start is the node the walk began at.
	Start string `json:"start"`
	// Direction is the edge direction that was followed.
	Direction string `json:"direction"`
	// MaxDepth is the hop bound the server applied.
	MaxDepth int `json:"max_depth"`
	// Limit is the node bound the server applied.
	Limit int `json:"limit"`
	// Count is the number of nodes returned.
	Count int `json:"count"`
	// Truncated reports that a bound stopped the walk early.
	Truncated bool `json:"truncated"`
	// Nodes are the visited nodes in BFS order.
	Nodes []GraphVisit `json:"nodes"`
}

// GraphPath is the outcome of a path search.
//
// Found false is an ordinary answer, not an error: either no path exists or
// the search stopped at a bound. Message says which — a cap-stopped search is
// inconclusive, not proof that no path exists — so do not read Found false as
// "there is definitely no path" without checking it.
//
// TotalCost is meaningful only for GraphWeightedShortestPath; it is zero for
// the unweighted search, which minimises hops.
type GraphPath struct {
	// Found reports whether a path was found.
	Found bool `json:"found"`
	// From is the start node id.
	From string `json:"from"`
	// To is the target node id.
	To string `json:"to"`
	// Direction is the edge direction that was followed.
	Direction string `json:"direction"`
	// Hops is the number of edges on the path.
	Hops int `json:"hops"`
	// NodePath is the node ids along the path, From first.
	NodePath []string `json:"node_path"`
	// EdgePath is the edge ids along the path.
	EdgePath []string `json:"edge_path"`
	// TotalCost is the summed weight, for the weighted search only.
	TotalCost float64 `json:"total_cost"`
	// Message says why Found is false, when it is.
	Message string `json:"message"`
}

// GraphNodePage is one page of GraphListNodes.
type GraphNodePage struct {
	// Graph is the graph name.
	Graph string `json:"graph"`
	// Count is the number of nodes on this page.
	Count int `json:"count"`
	// Nodes are this page's nodes.
	Nodes []GraphNode `json:"nodes"`
	// Truncated reports that more nodes remain past this page.
	Truncated bool `json:"truncated"`
	// Total is the graph's full node count.
	Total int64 `json:"total"`
}

// GraphEdgePage is one page of GraphListEdges.
type GraphEdgePage struct {
	// Graph is the graph name.
	Graph string `json:"graph"`
	// Count is the number of edges on this page.
	Count int `json:"count"`
	// Edges are this page's edges.
	Edges []GraphEdge `json:"edges"`
	// Truncated reports that more edges remain past this page.
	Truncated bool `json:"truncated"`
	// Total is the graph's full edge count.
	Total int64 `json:"total"`
}

// GraphRows is the result of a Cypher query: named columns and their rows, with
// each cell left as decoded JSON because a Cypher RETURN can yield a scalar, a
// list, or a whole node.
type GraphRows struct {
	// Graph is the graph that was queried.
	Graph string `json:"graph"`
	// Columns are the RETURN column names.
	Columns []string `json:"columns"`
	// Rows are the result rows, each cell as decoded JSON.
	Rows [][]any `json:"rows"`
	// Count is the number of rows returned.
	Count int `json:"count"`
	// Truncated reports that the server's row cap cut the result short.
	Truncated bool `json:"truncated"`
}

// NeighborOptions narrows GraphNeighbors. The zero value follows outgoing
// edges of every label, up to the server's cap.
type NeighborOptions struct {
	// Direction defaults to DirectionOutgoing when empty.
	Direction GraphDirection
	// Label keeps only edges with this label. Empty means every label.
	Label string
	// Limit caps the neighbors returned. Zero or less asks for the server
	// default (10,000, which is also the hard cap).
	Limit int
}

// TraverseOptions bounds a GraphTraverse. The zero value walks outgoing edges
// of every label to the server's default depth (3) and node limit (100).
type TraverseOptions struct {
	// Direction defaults to DirectionOutgoing when empty.
	Direction GraphDirection
	// Label keeps only edges with this label. Empty means every label.
	Label string
	// MaxDepth bounds the hop count. Zero or less asks for the server default
	// (3); the server clamps anything above 10.
	MaxDepth int
	// Limit caps the nodes returned. Zero or less asks for the server default
	// (100); the server clamps to 1..=1000.
	Limit int
}

// PathOptions bounds a GraphShortestPath. The zero value searches outgoing
// edges of every label to the server's default depth (10).
type PathOptions struct {
	// Direction defaults to DirectionOutgoing when empty.
	Direction GraphDirection
	// Label keeps only edges with this label. Empty means every label.
	Label string
	// MaxDepth bounds the hop count. Zero or less asks for the server default,
	// which is also its maximum (10).
	MaxDepth int
}

// WeightedPathOptions bounds a GraphWeightedShortestPath.
//
// There is deliberately no MaxDepth: a weighted search is bounded by settled
// cost, not by hop count, and a depth clamp would silently discard the
// cheap-but-long path that is the whole reason to run one.
type WeightedPathOptions struct {
	// Direction defaults to DirectionOutgoing when empty.
	Direction GraphDirection
	// Label keeps only edges with this label. Empty means every label.
	Label string
	// WeightProperty names the edge property holding the cost. Empty means
	// "weight". An edge missing it, or holding a non-number, weighs 1.0; a
	// negative weight is refused rather than mis-solved.
	WeightProperty string
}

func graphOp(body any) map[string]any {
	return map[string]any{"Graph": body}
}

// putDirection writes direction into op only when the caller chose one, so an
// empty GraphDirection lands on the server's default instead of failing to
// deserialize as the empty string.
func putDirection(op map[string]any, d GraphDirection) {
	if d != "" {
		op["direction"] = d
	}
}

func putOptional(op map[string]any, key string, value int) {
	if value > 0 {
		op[key] = value
	}
}

func putLabel(op map[string]any, label string) {
	if label != "" {
		op["label"] = label
	}
}

// -- graphs ----------------------------------------------------------------

// GraphCreate creates an empty graph.
func (c *Client) GraphCreate(graph string) error {
	_, err := c.request(graphOp(map[string]any{
		"CreateGraph": map[string]any{"graph": graph},
	}))
	return err
}

// GraphDrop removes a graph with its nodes and edges. Dropping a graph that
// does not exist is an error.
func (c *Client) GraphDrop(graph string) error {
	_, err := c.request(graphOp(map[string]any{
		"DropGraph": map[string]any{"graph": graph},
	}))
	return err
}

// GraphList names every graph in the database.
func (c *Client) GraphList() ([]string, error) {
	resp, err := c.request(graphOp("ListGraphs"))
	if err != nil {
		return nil, err
	}
	var out struct {
		Graphs []string `json:"graphs"`
	}
	if err := decodeJSON(resp, "ListGraphs", &out); err != nil {
		return nil, err
	}
	return out.Graphs, nil
}

// -- nodes -----------------------------------------------------------------

// GraphAddNode stores a node, replacing any node already under id. labels and
// properties may be nil.
func (c *Client) GraphAddNode(graph, id string, labels []string, properties map[string]any) error {
	if labels == nil {
		labels = []string{}
	}
	_, err := c.request(graphOp(map[string]any{
		"AddNode": map[string]any{
			"graph":      graph,
			"id":         id,
			"labels":     labels,
			"properties": properties,
		},
	}))
	return err
}

// GraphGetNode fetches one node by id. found is false when no such node exists
// — the server answers a miss with a null payload, which would otherwise decode
// into a zero-valued GraphNode indistinguishable from a real one.
func (c *Client) GraphGetNode(graph, id string) (node *GraphNode, found bool, err error) {
	resp, err := c.request(graphOp(map[string]any{
		"GetNode": map[string]any{"graph": graph, "id": id},
	}))
	if err != nil {
		return nil, false, err
	}
	if resp.DataKind != "Json" {
		return nil, false, &ProtocolError{Message: fmt.Sprintf("expected Json for GetNode, got %s", kindOrRaw(resp))}
	}
	if isNullPayload(resp.DataRaw) {
		return nil, false, nil
	}
	var out GraphNode
	if err := decodeJSON(resp, "GetNode", &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// GraphDeleteNode removes a node by id. Deleting an absent node is not an
// error; a missing graph is.
func (c *Client) GraphDeleteNode(graph, id string) error {
	_, err := c.request(graphOp(map[string]any{
		"DeleteNode": map[string]any{"graph": graph, "id": id},
	}))
	return err
}

// GraphListNodes reads one page of a graph's nodes, ordered by id. limit <= 0
// asks for the server default (500, capped at 1000); offset skips that many.
func (c *Client) GraphListNodes(graph string, limit, offset int) (*GraphNodePage, error) {
	op := map[string]any{"graph": graph}
	putOptional(op, "limit", limit)
	putOptional(op, "offset", offset)
	resp, err := c.request(graphOp(map[string]any{"ListNodes": op}))
	if err != nil {
		return nil, err
	}
	var out GraphNodePage
	if err := decodeJSON(resp, "ListNodes", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -- edges -----------------------------------------------------------------

// GraphAddEdge stores a directed edge from -> to under id. Both endpoints must
// already exist; a dangling endpoint is refused rather than created. properties
// may be nil.
func (c *Client) GraphAddEdge(graph, id, from, to, label string, properties map[string]any) error {
	_, err := c.request(graphOp(map[string]any{
		"AddEdge": map[string]any{
			"graph":      graph,
			"id":         id,
			"from":       from,
			"to":         to,
			"label":      label,
			"properties": properties,
		},
	}))
	return err
}

// GraphGetEdge fetches one edge by id. found is false when no such edge exists.
func (c *Client) GraphGetEdge(graph, id string) (edge *GraphEdge, found bool, err error) {
	resp, err := c.request(graphOp(map[string]any{
		"GetEdge": map[string]any{"graph": graph, "id": id},
	}))
	if err != nil {
		return nil, false, err
	}
	if resp.DataKind != "Json" {
		return nil, false, &ProtocolError{Message: fmt.Sprintf("expected Json for GetEdge, got %s", kindOrRaw(resp))}
	}
	if isNullPayload(resp.DataRaw) {
		return nil, false, nil
	}
	var out GraphEdge
	if err := decodeJSON(resp, "GetEdge", &out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// GraphDeleteEdge removes an edge by id. Deleting an absent edge is not an
// error; a missing graph is.
func (c *Client) GraphDeleteEdge(graph, id string) error {
	_, err := c.request(graphOp(map[string]any{
		"DeleteEdge": map[string]any{"graph": graph, "id": id},
	}))
	return err
}

// GraphListEdges reads one page of a graph's edges, ordered by id. limit <= 0
// asks for the server default (500, capped at 1000); offset skips that many.
func (c *Client) GraphListEdges(graph string, limit, offset int) (*GraphEdgePage, error) {
	op := map[string]any{"graph": graph}
	putOptional(op, "limit", limit)
	putOptional(op, "offset", offset)
	resp, err := c.request(graphOp(map[string]any{"ListEdges": op}))
	if err != nil {
		return nil, err
	}
	var out GraphEdgePage
	if err := decodeJSON(resp, "ListEdges", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// -- traversal -------------------------------------------------------------

// GraphNeighbors lists the edges incident to a node and the node at the far end
// of each. A node with no matching edges yields an empty slice, not an error.
func (c *Client) GraphNeighbors(graph, nodeID string, opts NeighborOptions) ([]GraphNeighbor, error) {
	op := map[string]any{"graph": graph, "node_id": nodeID}
	putDirection(op, opts.Direction)
	putLabel(op, opts.Label)
	putOptional(op, "limit", opts.Limit)
	resp, err := c.request(graphOp(map[string]any{"Neighbors": op}))
	if err != nil {
		return nil, err
	}
	var out struct {
		Neighbors []GraphNeighbor `json:"neighbors"`
	}
	if err := decodeJSON(resp, "Neighbors", &out); err != nil {
		return nil, err
	}
	return out.Neighbors, nil
}

// GraphDegree counts the edges incident to a node in a direction.
// DirectionBoth counts each edge once, self-loops included.
func (c *Client) GraphDegree(graph, nodeID string, direction GraphDirection) (int, error) {
	op := map[string]any{"graph": graph, "node_id": nodeID}
	putDirection(op, direction)
	resp, err := c.request(graphOp(map[string]any{"Degree": op}))
	if err != nil {
		return 0, err
	}
	var out struct {
		Degree int `json:"degree"`
	}
	if err := decodeJSON(resp, "Degree", &out); err != nil {
		return 0, err
	}
	return out.Degree, nil
}

// GraphTraverse walks outward from start by breadth-first search, bounded by
// opts. The start node must exist.
func (c *Client) GraphTraverse(graph, start string, opts TraverseOptions) (*GraphTraversal, error) {
	op := map[string]any{"graph": graph, "start": start}
	putDirection(op, opts.Direction)
	putLabel(op, opts.Label)
	putOptional(op, "max_depth", opts.MaxDepth)
	putOptional(op, "limit", opts.Limit)
	resp, err := c.request(graphOp(map[string]any{"Traverse": op}))
	if err != nil {
		return nil, err
	}
	var out GraphTraversal
	if err := decodeJSON(resp, "Traverse", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphShortestPath finds a path from `from` to `to` with the fewest hops.
// Both endpoints must exist. "No path" comes back as a GraphPath with Found
// false, not as an error.
func (c *Client) GraphShortestPath(graph, from, to string, opts PathOptions) (*GraphPath, error) {
	op := map[string]any{"graph": graph, "from": from, "to": to}
	putDirection(op, opts.Direction)
	putLabel(op, opts.Label)
	putOptional(op, "max_depth", opts.MaxDepth)
	resp, err := c.request(graphOp(map[string]any{"ShortestPath": op}))
	if err != nil {
		return nil, err
	}
	var out GraphPath
	if err := decodeJSON(resp, "ShortestPath", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphWeightedShortestPath finds the least-cost path from `from` to `to` by
// summed edge weight.
//
// This is a different question from GraphShortestPath, which minimises hops:
// with unequal weights the two return different paths and neither substitutes
// for the other. "No path" comes back as Found false, not as an error.
func (c *Client) GraphWeightedShortestPath(graph, from, to string, opts WeightedPathOptions) (*GraphPath, error) {
	op := map[string]any{"graph": graph, "from": from, "to": to}
	putDirection(op, opts.Direction)
	putLabel(op, opts.Label)
	if opts.WeightProperty != "" {
		op["weight_property"] = opts.WeightProperty
	}
	resp, err := c.request(graphOp(map[string]any{"WeightedShortestPath": op}))
	if err != nil {
		return nil, err
	}
	var out GraphPath
	if err := decodeJSON(resp, "WeightedShortestPath", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphQuery runs a read-only Cypher-subset query and returns its rows.
//
// The server implements MATCH / WHERE / RETURN with labels, property
// predicates, relationship direction and type, bounded variable-length paths,
// DISTINCT, ORDER BY / SKIP / LIMIT, and global aggregates. Every other clause
// — all write clauses, OPTIONAL MATCH, WITH, UNWIND, CALL, path variables,
// shortestPath(), parameters — is refused by name rather than ignored, so a
// rejected query returns an error instead of an answer computed from a
// partially-understood statement.
func (c *Client) GraphQuery(graph, cypher string) (*GraphRows, error) {
	resp, err := c.request(graphOp(map[string]any{
		"Query": map[string]any{"graph": graph, "cypher": cypher},
	}))
	if err != nil {
		return nil, err
	}
	var out GraphRows
	if err := decodeJSON(resp, "Query", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
