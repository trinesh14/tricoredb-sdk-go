package tricoredb

import (
	"encoding/json"
	"fmt"
)

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

// -- added for server 0.1.0-rc.3 ---------------------------------------------
//
// Everything below needs tricore-server 0.1.0-rc.3 or later. An older server
// does not know these operations and answers each with an error.

// ComponentKind is how GraphConnectedComponents groups nodes. Empty leaves the
// server's default, ComponentWeak.
type ComponentKind string

const (
	// ComponentWeak ignores edge direction.
	ComponentWeak ComponentKind = "weak"
	// ComponentStrong groups nodes that can each reach the other along edge
	// direction.
	ComponentStrong ComponentKind = "strong"
)

// ConstraintKind is the rule a constraint enforces.
type ConstraintKind string

const (
	// ConstraintUnique: no two records with the label share a value for the
	// property.
	ConstraintUnique ConstraintKind = "unique"
	// ConstraintRequired: every record with the label has the property.
	ConstraintRequired ConstraintKind = "required"
)

// ConstraintTarget is what a constraint applies to. Empty leaves the server's
// default, TargetNode.
type ConstraintTarget string

const (
	TargetNode ConstraintTarget = "node"
	TargetEdge ConstraintTarget = "edge"
)

// CentralityMeasure is the measure GraphCentrality computes.
type CentralityMeasure string

const (
	CentralityBetweenness CentralityMeasure = "betweenness"
	CentralityCloseness   CentralityMeasure = "closeness"
	CentralityEigenvector CentralityMeasure = "eigenvector"
)

// SimilarityMetric is how GraphNodeSimilarity compares neighbour sets. Empty
// leaves the server's default, SimilarityJaccard.
type SimilarityMetric string

const (
	// SimilarityJaccard is shared neighbours over all neighbours.
	SimilarityJaccard SimilarityMetric = "jaccard"
	// SimilarityOverlap is shared neighbours over the smaller set.
	SimilarityOverlap SimilarityMetric = "overlap"
)

// GraphNodeInput is one node of GraphAddMany.
type GraphNodeInput struct {
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Properties map[string]any `json:"properties,omitempty"`
}

// GraphEdgeInput is one edge of GraphAddMany.
type GraphEdgeInput struct {
	ID         string         `json:"id"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Label      string         `json:"label"`
	Properties map[string]any `json:"properties,omitempty"`
}

// GraphCounts is how many nodes and edges a batch write or a copy handled.
type GraphCounts struct {
	Nodes int64 `json:"nodes"`
	Edges int64 `json:"edges"`
}

// GraphRemoved is what GraphRemoveMany deleted.
type GraphRemoved struct {
	NodesRemoved int64 `json:"nodes_removed"`
	// EdgesRemoved includes the edges detached with a node.
	EdgesRemoved int64 `json:"edges_removed"`
}

// NodeUpdate is what GraphUpdateNode changes. The zero value changes nothing.
type NodeUpdate struct {
	// Properties is a JSON merge patch: a key set to a value is set, a key
	// set to nil is removed, and a key left out is left alone.
	Properties map[string]any
	// AddLabels are added; adding one the node already has is a no-op.
	AddLabels []string
	// RemoveLabels are removed, before the additions.
	RemoveLabels []string
}

// ComponentOptions narrows GraphConnectedComponents.
type ComponentOptions struct {
	Kind ComponentKind
	// Label considers only edges with this label.
	Label string
	// Limit caps the components listed, never the number counted (default
	// 100, capped at 1000).
	Limit int
}

// GraphGroup is one group of nodes: a connected component, or a community.
type GraphGroup struct {
	Nodes []string `json:"nodes"`
	// Size is how many nodes the group has, which may be more than are listed.
	Size      int  `json:"size"`
	Truncated bool `json:"truncated"`
}

// GraphComponents is the connected components of a graph, largest first.
type GraphComponents struct {
	Kind ComponentKind `json:"kind"`
	// Count is how many components the graph has.
	Count   int `json:"count"`
	Largest int `json:"largest"`
	// Truncated says more components exist than are listed.
	Truncated  bool         `json:"truncated"`
	Components []GraphGroup `json:"components"`
}

// GraphCycle is one cycle, as the evidence GraphDetectCycle returns.
type GraphCycle struct {
	Nodes []string `json:"nodes"`
	Edges []string `json:"edges"`
}

// GraphCycleCheck is the answer to GraphDetectCycle.
type GraphCycleCheck struct {
	HasCycle bool `json:"hasCycle"`
	Directed bool `json:"directed"`
	// Cycle is one cycle when there is one, and nil otherwise.
	Cycle *GraphCycle `json:"cycle"`
}

// GraphStatistics is counts describing a graph.
type GraphStatistics struct {
	Nodes int64 `json:"nodes"`
	Edges int64 `json:"edges"`
	// NodeLabels counts nodes per label; EdgeTypes counts edges per type.
	NodeLabels     map[string]int64 `json:"nodeLabels"`
	EdgeTypes      map[string]int64 `json:"edgeTypes"`
	UnlabeledNodes int64            `json:"unlabeledNodes"`
	SelfLoops      int64            `json:"selfLoops"`
	IsolatedNodes  int64            `json:"isolatedNodes"`
	AvgOutDegree   float64          `json:"avgOutDegree"`
	MaxOutDegree   int64            `json:"maxOutDegree"`
	MaxInDegree    int64            `json:"maxInDegree"`
	// DanglingEdges counts edges whose endpoint no longer exists.
	DanglingEdges int64 `json:"danglingEdges"`
}

// GraphSubgraphResult is the part of a graph around some seed nodes.
type GraphSubgraphResult struct {
	Direction string `json:"direction"`
	MaxDepth  int    `json:"max_depth"`
	Limit     int    `json:"limit"`
	NodeCount int    `json:"node_count"`
	EdgeCount int    `json:"edge_count"`
	// Nodes are the nodes reached, each with the depth it was reached at.
	Nodes []GraphVisit `json:"nodes"`
	// Edges are every edge joining two of those nodes.
	Edges          []GraphEdge `json:"edges"`
	NodesTruncated bool        `json:"nodes_truncated"`
	EdgesTruncated bool        `json:"edges_truncated"`
}

// GraphPropertySchema is one property key seen on a label or an edge type.
type GraphPropertySchema struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
	// Types are the JSON types seen under the key.
	Types []string `json:"types"`
}

// GraphLabelSchema is one node label, or the unlabelled nodes, in a
// GraphSchemaInfo.
type GraphLabelSchema struct {
	// Label is empty for the unlabelled nodes.
	Label      string                `json:"label"`
	Count      int64                 `json:"count"`
	Properties []GraphPropertySchema `json:"properties"`
}

// GraphEdgeTypeSchema is one edge type in a GraphSchemaInfo.
type GraphEdgeTypeSchema struct {
	Type       string                `json:"type"`
	Count      int64                 `json:"count"`
	Properties []GraphPropertySchema `json:"properties"`
}

// GraphSchemaInfo is what a graph's data looks like.
type GraphSchemaInfo struct {
	Nodes          int64                 `json:"nodes"`
	Edges          int64                 `json:"edges"`
	NodeLabels     []GraphLabelSchema    `json:"node_labels"`
	EdgeTypes      []GraphEdgeTypeSchema `json:"edge_types"`
	UnlabeledNodes GraphLabelSchema      `json:"unlabeled_nodes"`
}

// KPathOptions narrows GraphKShortestPaths.
type KPathOptions struct {
	// K is how many paths to return (default 3, capped at 20).
	K         int
	Direction GraphDirection
	Label     string
	// WeightProperty names the edge property holding the cost. Without it
	// every edge costs 1, so paths are ranked by hops.
	WeightProperty string
}

// GraphPathEntry is one path of a GraphPaths.
type GraphPathEntry struct {
	// Nodes are the node ids along the path, the start first.
	Nodes []string `json:"nodes"`
	Edges []string `json:"edges"`
	Hops  int      `json:"hops"`
	// Cost is the summed cost of the path's edges.
	Cost float64 `json:"cost"`
}

// GraphPaths is several paths between two nodes.
type GraphPaths struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	Direction string           `json:"direction"`
	Count     int              `json:"count"`
	Paths     []GraphPathEntry `json:"paths"`
	// Complete, from GraphAllPaths, says whether the enumeration finished. It
	// is nil from a search that does not report it.
	Complete *bool `json:"complete"`
	// StoppedBy names what stopped an enumeration that did not finish.
	StoppedBy string `json:"stopped_by"`
}

// CentralityOptions narrows GraphCentrality.
type CentralityOptions struct {
	// Undirected treats every edge as two-way; the server follows edge
	// direction by default.
	Undirected bool
	Label      string
	// Limit is how many nodes to return (default 20, capped at 1000).
	Limit int
	// Iterations and Tolerance tune eigenvector centrality only.
	Iterations int
	Tolerance  float64
}

// GraphScore is one node and the score an algorithm gave it.
type GraphScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

// GraphCentralityResult is the answer to GraphCentrality.
type GraphCentralityResult struct {
	Measure  CentralityMeasure `json:"measure"`
	Directed bool              `json:"directed"`
	// Nodes is how many nodes the graph holds.
	Nodes   int64        `json:"nodes"`
	Results []GraphScore `json:"results"`
	// Converged and Iterations are reported for eigenvector centrality only.
	Converged  *bool `json:"converged"`
	Iterations int   `json:"iterations"`
}

// CommunityOptions narrows GraphCommunities.
type CommunityOptions struct {
	Label      string
	Iterations int
	// Limit caps the communities listed.
	Limit int
}

// GraphCommunitiesResult is the communities of a graph, largest first.
type GraphCommunitiesResult struct {
	Algorithm   string       `json:"algorithm"`
	Count       int          `json:"count"`
	Modularity  float64      `json:"modularity"`
	Converged   bool         `json:"converged"`
	Iterations  int          `json:"iterations"`
	Communities []GraphGroup `json:"communities"`
}

// GraphTriangles is the answer to GraphTriangleCount.
type GraphTriangles struct {
	Triangles         int64   `json:"triangles"`
	Transitivity      float64 `json:"transitivity"`
	AverageClustering float64 `json:"average_clustering"`
	Nodes             []struct {
		ID         string  `json:"id"`
		Triangles  int64   `json:"triangles"`
		Clustering float64 `json:"clustering"`
	} `json:"nodes"`
}

// SimilarityOptions narrows GraphNodeSimilarity.
type SimilarityOptions struct {
	// Direction says which edges define a node's neighbours. The server's
	// default is both.
	Direction GraphDirection
	Label     string
	Metric    SimilarityMetric
	Limit     int
}

// GraphFindings is one class of finding in a GraphConsistency.
type GraphFindings struct {
	Count int64 `json:"count"`
	// Listed are the ones listed, as the server describes them.
	Listed []json.RawMessage `json:"listed"`
}

// GraphConsistency is the answer to GraphCheckConsistency.
type GraphConsistency struct {
	// Consistent says nothing was found.
	Consistent bool  `json:"consistent"`
	Nodes      int64 `json:"nodes"`
	Edges      int64 `json:"edges"`
	// DanglingEdges are edges whose endpoint no longer exists.
	DanglingEdges GraphFindings `json:"dangling_edges"`
	Undecodable   GraphFindings `json:"undecodable"`
	// Misfiled are records stored under another id.
	Misfiled GraphFindings `json:"misfiled"`
	// Mirror compares the resident mirror with storage.
	Mirror struct {
		Loaded      bool          `json:"loaded"`
		Differences GraphFindings `json:"differences"`
	} `json:"mirror"`
}

// GraphIndex is one node index.
type GraphIndex struct {
	Name string `json:"name"`
	// Label is the label the index is limited to, and empty when it is not.
	Label      string   `json:"label"`
	Properties []string `json:"properties"`
}

// GraphConstraint is one constraint.
type GraphConstraint struct {
	Name     string           `json:"name"`
	Label    string           `json:"label"`
	Property string           `json:"property"`
	Kind     ConstraintKind   `json:"kind"`
	Target   ConstraintTarget `json:"target"`
}

// GraphSchemaObjects is a graph's indexes and constraints.
type GraphSchemaObjects struct {
	Indexes     []GraphIndex      `json:"indexes"`
	Constraints []GraphConstraint `json:"constraints"`
}

// GraphFoundNodes is the answer to GraphFindNodes.
type GraphFoundNodes struct {
	Count int         `json:"count"`
	Nodes []GraphNode `json:"nodes"`
	// Plan says how the request was answered: an index by name, the label
	// index, or a scan.
	Plan      string `json:"plan"`
	Truncated bool   `json:"truncated"`
}

// PageRankOptions narrows GraphPageRank. Zero values leave the server's
// defaults.
type PageRankOptions struct {
	// Damping must lie in [0, 1) (default 0.85).
	Damping float64
	// Iterations bounds the iteration (default 20, capped at 100).
	Iterations int
	Tolerance  float64
	Label      string
	// Limit is how many nodes to return (default 20, capped at 1000).
	Limit int
}

// GraphPageRankResult is the answer to GraphPageRank.
type GraphPageRankResult struct {
	// Ranks are the top nodes. The ranks of the whole graph sum to 1.
	Ranks []GraphScore `json:"ranks"`
	// Count is how many nodes were ranked.
	Count      int     `json:"count"`
	Damping    float64 `json:"damping"`
	Iterations int     `json:"iterations"`
	Converged  bool    `json:"converged"`
	Truncated  bool    `json:"truncated"`
}

// GraphWriteSummary is what a Cypher write changed.
//
// Inside a transaction a statement is queued rather than applied: the counts
// are then zero and Queued says how many statements the transaction holds.
type GraphWriteSummary struct {
	Statements           int64 `json:"statements"`
	NodesCreated         int64 `json:"nodesCreated"`
	NodesDeleted         int64 `json:"nodesDeleted"`
	RelationshipsCreated int64 `json:"relationshipsCreated"`
	RelationshipsDeleted int64 `json:"relationshipsDeleted"`
	PropertiesSet        int64 `json:"propertiesSet"`
	LabelsAdded          int64 `json:"labelsAdded"`
	LabelsRemoved        int64 `json:"labelsRemoved"`
	// Queued is set inside a transaction; Committed is set by a commit.
	Queued    int64 `json:"queued"`
	Committed int64 `json:"committed"`
}

// GraphAddMany writes many nodes and edges as one unit: every record lands or
// none does.
//
// Nodes are written before edges, so an edge may join nodes created in the
// same batch. The whole batch is validated before anything is written.
func (c *Client) GraphAddMany(graph string, nodes []GraphNodeInput, edges []GraphEdgeInput) (*GraphCounts, error) {
	wireNodes := make([]GraphNodeInput, len(nodes))
	for i, n := range nodes {
		if n.Labels == nil {
			n.Labels = []string{}
		}
		wireNodes[i] = n
	}
	if edges == nil {
		edges = []GraphEdgeInput{}
	}
	var out GraphCounts
	op := graphOp(map[string]any{"AddMany": map[string]any{"graph": graph, "nodes": wireNodes, "edges": edges}})
	if err := c.requestJSON(op, "AddMany", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphUpdateNode changes an existing node in place, and returns it as it now
// stands.
func (c *Client) GraphUpdateNode(graph, id string, update NodeUpdate) (*GraphNode, error) {
	body := map[string]any{"graph": graph, "id": id}
	if update.Properties != nil {
		body["properties"] = update.Properties
	}
	if len(update.AddLabels) > 0 {
		body["add_labels"] = update.AddLabels
	}
	if len(update.RemoveLabels) > 0 {
		body["remove_labels"] = update.RemoveLabels
	}
	var out GraphNode
	if err := c.requestJSON(graphOp(map[string]any{"UpdateNode": body}), "UpdateNode", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphUpdateEdge changes an existing edge in place, and returns it as it now
// stands. properties is a JSON merge patch (nil changes none) and a non-empty
// label changes the edge's type; the endpoints cannot change.
func (c *Client) GraphUpdateEdge(graph, id string, properties map[string]any, label string) (*GraphEdge, error) {
	body := map[string]any{"graph": graph, "id": id}
	if properties != nil {
		body["properties"] = properties
	}
	putLabel(body, label)
	var out GraphEdge
	if err := c.requestJSON(graphOp(map[string]any{"UpdateEdge": body}), "UpdateEdge", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphDetectCycle reports whether the graph contains a cycle, with one cycle
// as evidence.
//
// The server follows edge direction unless undirected is set; undirected, two
// parallel edges between a pair are a cycle and a single edge is not. An empty
// label considers every edge.
func (c *Client) GraphDetectCycle(graph string, undirected bool, label string) (*GraphCycleCheck, error) {
	body := map[string]any{"graph": graph}
	if undirected {
		body["directed"] = false
	}
	putLabel(body, label)
	var out GraphCycleCheck
	if err := c.requestJSON(graphOp(map[string]any{"DetectCycle": body}), "DetectCycle", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphCreateConstraint enforces a rule on a property of every node — or, with
// TargetEdge, every relationship — labelled label. An empty target is
// TargetNode. Refused when a stored record already breaks it.
func (c *Client) GraphCreateConstraint(graph, name, label, property string, kind ConstraintKind, target ConstraintTarget) error {
	body := map[string]any{
		"graph":    graph,
		"name":     name,
		"label":    label,
		"property": property,
		"kind":     kind,
	}
	if target != "" {
		body["target"] = target
	}
	_, err := c.request(graphOp(map[string]any{"CreateConstraint": body}))
	return err
}

// GraphFindNodes returns nodes with label, when non-empty, whose properties
// equal the given values, in id order. Plan says whether an index answered.
// limit <= 0 leaves the cap to the server (100).
func (c *Client) GraphFindNodes(graph, label string, properties map[string]any, limit int) (*GraphFoundNodes, error) {
	body := map[string]any{"graph": graph}
	putLabel(body, label)
	if len(properties) > 0 {
		body["properties"] = properties
	}
	putOptional(body, "limit", limit)
	var out GraphFoundNodes
	if err := c.requestJSON(graphOp(map[string]any{"FindNodes": body}), "FindNodes", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphBeginTransaction opens a transaction on a graph, and returns its id.
//
// Pass the id to SetRequestTxn: every GraphExecute sent while it is set is
// queued, and the commit applies them all or none.
func (c *Client) GraphBeginTransaction(graph string) (string, error) {
	resp, err := c.requestIn(graphOp(map[string]any{"BeginTransaction": map[string]any{"graph": graph}}), "")
	if err != nil {
		return "", err
	}
	var out struct {
		Transaction string `json:"transaction"`
	}
	if err := decodeJSON(resp, "BeginTransaction", &out); err != nil {
		return "", err
	}
	if out.Transaction == "" {
		return "", &ProtocolError{Message: "BeginTransaction returned no transaction id"}
	}
	return out.Transaction, nil
}

// GraphCommitTransaction applies every statement queued in a transaction as
// one write: all of them or none. It stops SetRequestTxn naming the
// transaction.
func (c *Client) GraphCommitTransaction(graph, txn string) (*GraphWriteSummary, error) {
	resp, err := c.requestIn(graphOp(map[string]any{"CommitTransaction": map[string]any{"graph": graph}}), txn)
	c.forgetTxn(txn)
	if err != nil {
		return nil, err
	}
	var out GraphWriteSummary
	if err := decodeJSON(resp, "CommitTransaction", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphRollbackTransaction discards a transaction and its queued statements,
// returning how many there were.
func (c *Client) GraphRollbackTransaction(graph, txn string) (int64, error) {
	resp, err := c.requestIn(graphOp(map[string]any{"RollbackTransaction": map[string]any{"graph": graph}}), txn)
	c.forgetTxn(txn)
	if err != nil {
		return 0, err
	}
	var out struct {
		RolledBack int64 `json:"rolled_back"`
	}
	return out.RolledBack, decodeJSON(resp, "RollbackTransaction", &out)
}

// GraphRemoveMany deletes many nodes and edges as one unit. An id that is not
// there is skipped. With detach, every edge touching a removed node goes too.
func (c *Client) GraphRemoveMany(graph string, nodes, edges []string, detach bool) (*GraphRemoved, error) {
	body := map[string]any{
		"graph":  graph,
		"nodes":  strs(nodes),
		"edges":  strs(edges),
		"detach": detach,
	}
	var out GraphRemoved
	if err := c.requestJSON(graphOp(map[string]any{"RemoveMany": body}), "RemoveMany", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphCopy copies a graph, with its nodes, edges and schema, to a new name.
// Refused when to exists.
func (c *Client) GraphCopy(graph, to string) (*GraphCounts, error) {
	var out GraphCounts
	if err := c.requestJSON(graphOp(map[string]any{"CopyGraph": map[string]any{"graph": graph, "to": to}}), "CopyGraph", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphDfs walks outward from a node depth-first. It returns exactly the nodes
// GraphTraverse does, in DFS preorder.
func (c *Client) GraphDfs(graph, start string, opts TraverseOptions) (*GraphTraversal, error) {
	body := map[string]any{"graph": graph, "start": start}
	if opts.Direction != "" {
		body["direction"] = opts.Direction
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.MaxDepth > 0 {
		body["max_depth"] = opts.MaxDepth
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphTraversal
	if err := c.requestJSON(graphOp(map[string]any{"Dfs": body}), "Dfs", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphConnectedComponents partitions the graph's nodes into connected
// components, largest first.
func (c *Client) GraphConnectedComponents(graph string, opts ComponentOptions) (*GraphComponents, error) {
	body := map[string]any{"graph": graph}
	if opts.Kind != "" {
		body["kind"] = opts.Kind
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphComponents
	if err := c.requestJSON(graphOp(map[string]any{"ConnectedComponents": body}), "ConnectedComponents", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphStats returns counts describing a graph.
func (c *Client) GraphStats(graph string) (*GraphStatistics, error) {
	var out GraphStatistics
	if err := c.requestJSON(graphOp(map[string]any{"GraphStats": map[string]any{"graph": graph}}), "GraphStats", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphSubgraph returns the part of the graph within some hops of seeds: the
// nodes reached and every edge joining two of them. The server's defaults
// differ from a traversal's: both directions, one hop, and up to 1000 nodes.
func (c *Client) GraphSubgraph(graph string, seeds []string, opts TraverseOptions) (*GraphSubgraphResult, error) {
	body := map[string]any{"graph": graph, "seeds": strs(seeds)}
	if opts.Direction != "" {
		body["direction"] = opts.Direction
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.MaxDepth > 0 {
		body["max_depth"] = opts.MaxDepth
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphSubgraphResult
	if err := c.requestJSON(graphOp(map[string]any{"Subgraph": body}), "Subgraph", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphSchema describes what the graph's data looks like: its node labels and
// edge types, each with a count and the property keys seen on it.
func (c *Client) GraphSchema(graph string) (*GraphSchemaInfo, error) {
	var out GraphSchemaInfo
	if err := c.requestJSON(graphOp(map[string]any{"GraphSchema": map[string]any{"graph": graph}}), "GraphSchema", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphKShortestPaths returns the k cheapest loopless paths between two nodes,
// cheapest first.
func (c *Client) GraphKShortestPaths(graph, from, to string, opts KPathOptions) (*GraphPaths, error) {
	body := map[string]any{
		"graph": graph,
		"from":  from,
		"to":    to,
	}
	if opts.K > 0 {
		body["k"] = opts.K
	}
	if opts.Direction != "" {
		body["direction"] = opts.Direction
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.WeightProperty != "" {
		body["weight_property"] = opts.WeightProperty
	}
	var out GraphPaths
	if err := c.requestJSON(graphOp(map[string]any{"KShortestPaths": body}), "KShortestPaths", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphAllPaths returns every simple path between two nodes within a hop bound.
// A work budget bounds the search, and Complete says whether it finished.
func (c *Client) GraphAllPaths(graph, from, to string, opts TraverseOptions) (*GraphPaths, error) {
	body := map[string]any{
		"graph": graph,
		"from":  from,
		"to":    to,
	}
	if opts.Direction != "" {
		body["direction"] = opts.Direction
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.MaxDepth > 0 {
		body["max_depth"] = opts.MaxDepth
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphPaths
	if err := c.requestJSON(graphOp(map[string]any{"AllPaths": body}), "AllPaths", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphCentrality computes betweenness, closeness or eigenvector centrality,
// top nodes first.
func (c *Client) GraphCentrality(graph string, measure CentralityMeasure, opts CentralityOptions) (*GraphCentralityResult, error) {
	body := map[string]any{"graph": graph, "measure": measure}
	if opts.Undirected {
		body["directed"] = false
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	if opts.Iterations > 0 {
		body["iterations"] = opts.Iterations
	}
	if opts.Tolerance != 0 {
		body["tolerance"] = opts.Tolerance
	}
	var out GraphCentralityResult
	if err := c.requestJSON(graphOp(map[string]any{"Centrality": body}), "Centrality", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphCommunities finds communities by Louvain modularity optimisation,
// direction ignored.
func (c *Client) GraphCommunities(graph string, opts CommunityOptions) (*GraphCommunitiesResult, error) {
	body := map[string]any{"graph": graph}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.Iterations > 0 {
		body["iterations"] = opts.Iterations
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphCommunitiesResult
	if err := c.requestJSON(graphOp(map[string]any{"Communities": body}), "Communities", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphTriangleCount counts triangles and clustering coefficients, direction
// ignored. An empty label considers every edge; limit <= 0 leaves the cap to
// the server.
func (c *Client) GraphTriangleCount(graph, label string, limit int) (*GraphTriangles, error) {
	body := map[string]any{"graph": graph}
	if label != "" {
		body["label"] = label
	}
	if limit > 0 {
		body["limit"] = limit
	}
	var out GraphTriangles
	if err := c.requestJSON(graphOp(map[string]any{"TriangleCount": body}), "TriangleCount", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphNodeSimilarity returns the nodes whose neighbour sets most resemble
// node's, most similar first.
func (c *Client) GraphNodeSimilarity(graph, node string, opts SimilarityOptions) ([]GraphScore, error) {
	body := map[string]any{"graph": graph, "node": node}
	if opts.Direction != "" {
		body["direction"] = opts.Direction
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.Metric != "" {
		body["metric"] = opts.Metric
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out struct {
		Results []GraphScore `json:"results"`
	}
	return out.Results, c.requestJSON(graphOp(map[string]any{"NodeSimilarity": body}), "NodeSimilarity", &out)
}

// GraphCheckConsistency checks a graph's stored records against each other and
// against the resident mirror.
func (c *Client) GraphCheckConsistency(graph string) (*GraphConsistency, error) {
	var out GraphConsistency
	if err := c.requestJSON(graphOp(map[string]any{"CheckConsistency": map[string]any{"graph": graph}}), "CheckConsistency", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphCreateNodeIndex indexes node properties for GraphFindNodes. Several
// properties make a composite index that serves an equality on all of them; a
// non-empty label limits it to nodes carrying that label.
func (c *Client) GraphCreateNodeIndex(graph, name, label string, properties ...string) error {
	body := map[string]any{
		"graph":      graph,
		"name":       name,
		"properties": strs(properties),
	}
	if label != "" {
		body["label"] = label
	}
	_, err := c.request(graphOp(map[string]any{"CreateNodeIndex": body}))
	return err
}

// GraphDropSchemaObject drops an index or a constraint by name.
func (c *Client) GraphDropSchemaObject(graph, name string) error {
	_, err := c.request(graphOp(map[string]any{"DropSchemaObject": map[string]any{"graph": graph, "name": name}}))
	return err
}

// GraphListSchemaObjects lists a graph's indexes and constraints.
func (c *Client) GraphListSchemaObjects(graph string) (*GraphSchemaObjects, error) {
	var out GraphSchemaObjects
	if err := c.requestJSON(graphOp(map[string]any{"ListSchemaObjects": map[string]any{"graph": graph}}), "ListSchemaObjects", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphImportCSV imports nodes and edges from CSV text as one atomic batch,
// through the same path as GraphAddMany, so constraints apply. An empty text is
// left out.
func (c *Client) GraphImportCSV(graph, nodes, edges string) (*GraphCounts, error) {
	body := map[string]any{"graph": graph}
	if nodes != "" {
		body["nodes"] = nodes
	}
	if edges != "" {
		body["edges"] = edges
	}
	var out GraphCounts
	if err := c.requestJSON(graphOp(map[string]any{"ImportCsv": body}), "ImportCsv", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphPageRank ranks nodes by PageRank over edge direction, top nodes first.
func (c *Client) GraphPageRank(graph string, opts PageRankOptions) (*GraphPageRankResult, error) {
	body := map[string]any{"graph": graph}
	if opts.Damping != 0 {
		body["damping"] = opts.Damping
	}
	if opts.Iterations > 0 {
		body["iterations"] = opts.Iterations
	}
	if opts.Tolerance != 0 {
		body["tolerance"] = opts.Tolerance
	}
	if opts.Label != "" {
		body["label"] = opts.Label
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out GraphPageRankResult
	if err := c.requestJSON(graphOp(map[string]any{"PageRank": body}), "PageRank", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GraphExecute runs a Cypher write: CREATE, MERGE, SET, REMOVE, [DETACH]
// DELETE, optionally after MATCH ... [WHERE ...]. It is separate from
// GraphQuery because it writes. While SetRequestTxn names a graph transaction
// the statement is queued rather than applied.
func (c *Client) GraphExecute(graph, cypher string) (*GraphWriteSummary, error) {
	var out GraphWriteSummary
	if err := c.requestJSON(graphOp(map[string]any{"Execute": map[string]any{"graph": graph, "cypher": cypher}}), "Execute", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
