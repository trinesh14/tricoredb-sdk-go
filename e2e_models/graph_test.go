package e2emodels

import (
	"errors"
	"testing"

	"github.com/trinesh14/tricoredb-sdk-go"
)

func newGraph(t *testing.T, db *tricoredb.Client) string {
	t.Helper()
	name := uniqueName("graph")
	mustNoErr(t, "GraphCreate", db.GraphCreate(name))
	t.Cleanup(func() { _ = db.GraphDrop(name) })
	return name
}

func TestGraphLifecycle(t *testing.T) {
	db := newClient(t)
	name := uniqueName("graph")

	mustNoErr(t, "create", db.GraphCreate(name))

	graphs, err := db.GraphList()
	mustNoErr(t, "list", err)
	if !contains(graphs, name) {
		t.Fatalf("created graph %q absent from %v", name, graphs)
	}

	if err := db.GraphCreate(name); err == nil {
		t.Error("creating an existing graph returned nil error")
	}

	mustNoErr(t, "drop", db.GraphDrop(name))
	graphs, err = db.GraphList()
	mustNoErr(t, "list after drop", err)
	if contains(graphs, name) {
		t.Errorf("dropped graph %q still listed in %v", name, graphs)
	}
	if err := db.GraphDrop(name); err == nil {
		t.Error("dropping an absent graph returned nil error")
	}
}

// TestGraphNodeIsADistinctValueWitness proves a node read returns that node's
// own labels and properties, not another node's and not a zero value.
func TestGraphNodeIsADistinctValueWitness(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	mustNoErr(t, "add n1", db.GraphAddNode(g, "n1",
		[]string{"Person", "Engineer"}, map[string]any{"name": "ada", "age": 36}))
	mustNoErr(t, "add n2", db.GraphAddNode(g, "n2",
		[]string{"Company"}, map[string]any{"name": "acme", "age": 120}))

	n1, found, err := db.GraphGetNode(g, "n1")
	mustNoErr(t, "get n1", err)
	if !found {
		t.Fatal("get n1: found=false for a node just added")
	}
	if n1 == nil {
		t.Fatal("get n1: nil node with found=true")
	}
	if n1.ID != "n1" {
		t.Errorf("ID = %q, want n1", n1.ID)
	}
	wantExactly(t, "n1 labels", n1.Labels, "Person", "Engineer")
	if n1.Properties["name"] != "ada" {
		t.Errorf("Properties[name] = %v, want ada", n1.Properties["name"])
	}
	if n1.Properties["name"] == "acme" {
		t.Error("n1 came back holding n2's properties — the read returns the wrong node")
	}

	n2, found, err := db.GraphGetNode(g, "n2")
	mustNoErr(t, "get n2", err)
	if !found || n2 == nil {
		t.Fatal("get n2 failed")
	}
	wantExactly(t, "n2 labels", n2.Labels, "Company")
	if n2.Properties["name"] != "acme" {
		t.Errorf("n2 Properties[name] = %v, want acme", n2.Properties["name"])
	}

	miss, found, err := db.GraphGetNode(g, "no-such-node")
	mustNoErr(t, "get missing", err)
	if found {
		t.Error("get of an absent node reported found=true")
	}
	if miss != nil {
		t.Errorf("get of an absent node returned %+v, want nil", miss)
	}

	mustNoErr(t, "delete n2", db.GraphDeleteNode(g, "n2"))
	if _, found, _ := db.GraphGetNode(g, "n2"); found {
		t.Error("deleted node still readable")
	}
	if _, found, _ := db.GraphGetNode(g, "n1"); !found {
		t.Error("delete removed the wrong node")
	}
	mustNoErr(t, "delete absent node", db.GraphDeleteNode(g, "n2"))
}

func TestGraphEdges(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	mustNoErr(t, "add a", db.GraphAddNode(g, "a", nil, nil))
	mustNoErr(t, "add b", db.GraphAddNode(g, "b", nil, nil))
	mustNoErr(t, "add e1", db.GraphAddEdge(g, "e1", "a", "b", "KNOWS",
		map[string]any{"since": 2019, "weight": 2.5}))

	e, found, err := db.GraphGetEdge(g, "e1")
	mustNoErr(t, "get e1", err)
	if !found || e == nil {
		t.Fatal("get e1: not found after add")
	}
	if e.ID != "e1" || e.From != "a" || e.To != "b" || e.Label != "KNOWS" {
		t.Errorf("edge = %+v, want {e1 a b KNOWS}", *e)
	}
	if e.Properties["since"] != float64(2019) {
		t.Errorf("Properties[since] = %v (%T), want 2019", e.Properties["since"], e.Properties["since"])
	}

	miss, found, err := db.GraphGetEdge(g, "no-such-edge")
	mustNoErr(t, "get missing edge", err)
	if found || miss != nil {
		t.Errorf("get of an absent edge returned found=%v %+v, want false/nil", found, miss)
	}

	mustNoErr(t, "delete e1", db.GraphDeleteEdge(g, "e1"))
	if _, found, _ := db.GraphGetEdge(g, "e1"); found {
		t.Error("deleted edge still readable")
	}
	mustNoErr(t, "delete absent edge", db.GraphDeleteEdge(g, "e1"))
}

func TestGraphNeighborsAndDegree(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	for _, id := range []string{"hub", "out1", "out2", "in1"} {
		mustNoErr(t, "add "+id, db.GraphAddNode(g, id, nil, nil))
	}
	mustNoErr(t, "e1", db.GraphAddEdge(g, "e1", "hub", "out1", "KNOWS", nil))
	mustNoErr(t, "e2", db.GraphAddEdge(g, "e2", "hub", "out2", "LIKES", nil))
	mustNoErr(t, "e3", db.GraphAddEdge(g, "e3", "in1", "hub", "KNOWS", nil))

	t.Run("default direction is outgoing", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{})
		mustNoErr(t, "neighbors", err)
		wantExactly(t, "outgoing neighbors", neighborNodeIDs(ns), "out1", "out2")
		for _, n := range ns {
			if n.Direction != tricoredb.DirectionOutgoing {
				t.Errorf("neighbor %+v has direction %q, want outgoing", n, n.Direction)
			}
		}
	})

	t.Run("incoming", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{
			Direction: tricoredb.DirectionIncoming,
		})
		mustNoErr(t, "neighbors", err)
		wantExactly(t, "incoming neighbors", neighborNodeIDs(ns), "in1")
	})

	t.Run("both", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{
			Direction: tricoredb.DirectionBoth,
		})
		mustNoErr(t, "neighbors", err)
		wantExactly(t, "both neighbors", neighborNodeIDs(ns), "out1", "out2", "in1")
	})

	t.Run("label filter", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{
			Direction: tricoredb.DirectionBoth,
			Label:     "LIKES",
		})
		mustNoErr(t, "neighbors", err)
		wantExactly(t, "LIKES neighbors", neighborNodeIDs(ns), "out2")
	})

	t.Run("label filter matching nothing", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{
			Direction: tricoredb.DirectionBoth,
			Label:     "NOSUCHLABEL",
		})
		mustNoErr(t, "neighbors", err)
		if len(ns) != 0 {
			t.Errorf("expected no neighbors, got %v", neighborNodeIDs(ns))
		}
	})

	t.Run("limit", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "hub", tricoredb.NeighborOptions{
			Direction: tricoredb.DirectionBoth,
			Limit:     1,
		})
		mustNoErr(t, "neighbors", err)
		if len(ns) != 1 {
			t.Errorf("limit 1 returned %d neighbors", len(ns))
		}
	})

	t.Run("degree", func(t *testing.T) {
		cases := []struct {
			dir  tricoredb.GraphDirection
			want int
		}{
			{tricoredb.DirectionOutgoing, 2},
			{tricoredb.DirectionIncoming, 1},
			{tricoredb.DirectionBoth, 3},
			{"", 2}, // empty means the server default, outgoing.
		}
		for _, c := range cases {
			got, err := db.GraphDegree(g, "hub", c.dir)
			mustNoErr(t, "degree", err)
			if got != c.want {
				t.Errorf("degree(%q) = %d, want %d", c.dir, got, c.want)
			}
		}
	})
}

func TestGraphTraverse(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	// A chain: t0 -> t1 -> t2 -> t3, plus a side branch off t0 with a
	// different label.
	for _, id := range []string{"t0", "t1", "t2", "t3", "side"} {
		mustNoErr(t, "add "+id, db.GraphAddNode(g, id, nil, nil))
	}
	mustNoErr(t, "c1", db.GraphAddEdge(g, "c1", "t0", "t1", "CHAIN", nil))
	mustNoErr(t, "c2", db.GraphAddEdge(g, "c2", "t1", "t2", "CHAIN", nil))
	mustNoErr(t, "c3", db.GraphAddEdge(g, "c3", "t2", "t3", "CHAIN", nil))
	mustNoErr(t, "s1", db.GraphAddEdge(g, "s1", "t0", "side", "SIDE", nil))

	t.Run("depth bound excludes the far node", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "t0", tricoredb.TraverseOptions{MaxDepth: 2})
		mustNoErr(t, "traverse", err)
		if tr == nil {
			t.Fatal("GraphTraverse returned nil with a nil error")
		}
		ids := visitIDs(tr.Nodes)
		// t3 is 3 hops away and must be genuinely absent at max_depth 2.
		wantExactly(t, "depth 2", ids, "t0", "t1", "t2", "side")
		if tr.MaxDepth != 2 {
			t.Errorf("MaxDepth echoed as %d, want 2", tr.MaxDepth)
		}
		for _, v := range tr.Nodes {
			want := map[string]int{"t0": 0, "t1": 1, "t2": 2, "side": 1}[v.ID]
			if v.Depth != want {
				t.Errorf("node %q at depth %d, want %d", v.ID, v.Depth, want)
			}
		}
	})

	t.Run("full depth reaches the end of the chain", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "t0", tricoredb.TraverseOptions{MaxDepth: 5})
		mustNoErr(t, "traverse", err)
		wantExactly(t, "depth 5", visitIDs(tr.Nodes), "t0", "t1", "t2", "t3", "side")
	})

	t.Run("label filter follows only that edge type", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "t0", tricoredb.TraverseOptions{
			MaxDepth: 5, Label: "SIDE",
		})
		mustNoErr(t, "traverse", err)
		wantExactly(t, "SIDE only", visitIDs(tr.Nodes), "t0", "side")
	})

	t.Run("incoming direction walks the other way", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "t3", tricoredb.TraverseOptions{
			MaxDepth: 5, Direction: tricoredb.DirectionIncoming,
		})
		mustNoErr(t, "traverse", err)
		wantExactly(t, "incoming from t3", visitIDs(tr.Nodes), "t3", "t2", "t1", "t0")
	})

	t.Run("limit truncates and says so", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "t0", tricoredb.TraverseOptions{MaxDepth: 5, Limit: 2})
		mustNoErr(t, "traverse", err)
		if len(tr.Nodes) != 2 {
			t.Errorf("limit 2 returned %d nodes (%v)", len(tr.Nodes), visitIDs(tr.Nodes))
		}
		if !tr.Truncated {
			t.Error("Truncated=false when a limit stopped the walk early")
		}
	})
}

// TestGraphShortestPathVsWeighted is a distinct-value witness across two
// operations: the graph is built so the fewest-hop path and the least-cost path
// are *different*. An implementation that routed both to the same server op, or
// ignored edge weights, returns the same path twice and fails here.
func TestGraphShortestPathVsWeighted(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	for _, id := range []string{"a", "b", "c", "d"} {
		mustNoErr(t, "add "+id, db.GraphAddNode(g, id, nil, nil))
	}
	// One expensive hop straight across...
	mustNoErr(t, "direct", db.GraphAddEdge(g, "direct", "a", "d", "R", map[string]any{"weight": 10}))
	// ...versus three cheap ones the long way round.
	mustNoErr(t, "ab", db.GraphAddEdge(g, "ab", "a", "b", "R", map[string]any{"weight": 1}))
	mustNoErr(t, "bc", db.GraphAddEdge(g, "bc", "b", "c", "R", map[string]any{"weight": 1}))
	mustNoErr(t, "cd", db.GraphAddEdge(g, "cd", "c", "d", "R", map[string]any{"weight": 1}))

	hops, err := db.GraphShortestPath(g, "a", "d", tricoredb.PathOptions{})
	mustNoErr(t, "shortest path", err)
	if hops == nil {
		t.Fatal("GraphShortestPath returned nil with a nil error")
	}
	if !hops.Found {
		t.Fatalf("no path found from a to d: %+v", *hops)
	}
	if hops.Hops != 1 {
		t.Errorf("fewest-hop path took %d hops, want 1", hops.Hops)
	}
	if len(hops.NodePath) != 2 || hops.NodePath[0] != "a" || hops.NodePath[1] != "d" {
		t.Errorf("fewest-hop node path = %v, want [a d]", hops.NodePath)
	}
	wantExactly(t, "fewest-hop edges", hops.EdgePath, "direct")

	cost, err := db.GraphWeightedShortestPath(g, "a", "d", tricoredb.WeightedPathOptions{})
	mustNoErr(t, "weighted shortest path", err)
	if !cost.Found {
		t.Fatalf("no weighted path found from a to d: %+v", *cost)
	}
	if cost.TotalCost != 3 {
		t.Errorf("least-cost path totals %v, want 3 (1+1+1, not the direct edge's 10)", cost.TotalCost)
	}
	if cost.Hops != 3 {
		t.Errorf("least-cost path took %d hops, want 3", cost.Hops)
	}
	if len(cost.NodePath) != 4 {
		t.Errorf("least-cost node path = %v, want [a b c d]", cost.NodePath)
	}
	wantExactly(t, "least-cost edges", cost.EdgePath, "ab", "bc", "cd")

	// The whole point: the two answers differ.
	if len(hops.NodePath) == len(cost.NodePath) {
		t.Errorf("fewest-hop %v and least-cost %v are the same path — "+
			"edge weights are not being used", hops.NodePath, cost.NodePath)
	}

	t.Run("custom weight property", func(t *testing.T) {
		// "cost" is on no edge, so every edge weighs 1.0 and the direct hop wins.
		p, err := db.GraphWeightedShortestPath(g, "a", "d", tricoredb.WeightedPathOptions{
			WeightProperty: "cost",
		})
		mustNoErr(t, "weighted path with custom property", err)
		if !p.Found || p.TotalCost != 1 {
			t.Errorf("with an absent weight property every edge weighs 1.0, so the "+
				"direct hop should cost 1; got %+v", *p)
		}
	})

	t.Run("no path is Found=false, not an error", func(t *testing.T) {
		p, err := db.GraphShortestPath(g, "d", "a", tricoredb.PathOptions{})
		mustNoErr(t, "shortest path with no route", err)
		if p == nil {
			t.Fatal("nil path with a nil error")
		}
		if p.Found {
			t.Errorf("found a path from d to a against edge direction: %+v", *p)
		}
		if len(p.NodePath) != 0 {
			t.Errorf("NodePath = %v for a path that was not found", p.NodePath)
		}
		if p.Message == "" {
			t.Error("Found=false with no Message explaining whether the search was " +
				"exhaustive or stopped at a bound")
		}
		// Reversing the direction finds it.
		p, err = db.GraphShortestPath(g, "d", "a", tricoredb.PathOptions{
			Direction: tricoredb.DirectionIncoming,
		})
		mustNoErr(t, "shortest path incoming", err)
		if !p.Found {
			t.Errorf("no incoming path from d to a: %+v", *p)
		}
	})
}

func TestGraphListNodesAndEdges(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	for _, id := range []string{"L1", "L2", "L3"} {
		mustNoErr(t, "add "+id, db.GraphAddNode(g, id, []string{"N"}, map[string]any{"tag": id}))
	}
	mustNoErr(t, "x1", db.GraphAddEdge(g, "x1", "L1", "L2", "E", nil))
	mustNoErr(t, "x2", db.GraphAddEdge(g, "x2", "L2", "L3", "E", nil))

	nodes, err := db.GraphListNodes(g, 0, 0)
	mustNoErr(t, "list nodes", err)
	if nodes == nil {
		t.Fatal("GraphListNodes returned nil with a nil error")
	}
	if nodes.Total != 3 || nodes.Count != 3 {
		t.Errorf("Total=%d Count=%d, want 3/3", nodes.Total, nodes.Count)
	}
	var ids []string
	for _, n := range nodes.Nodes {
		ids = append(ids, n.ID)
	}
	wantExactly(t, "nodes", ids, "L1", "L2", "L3")
	if nodes.Nodes[0].Properties["tag"] != "L1" {
		t.Errorf("first node properties = %v, want tag L1 (nodes are id-ordered)", nodes.Nodes[0].Properties)
	}

	page, err := db.GraphListNodes(g, 1, 1)
	mustNoErr(t, "list nodes paged", err)
	if len(page.Nodes) != 1 || page.Nodes[0].ID != "L2" {
		t.Errorf("limit=1 offset=1 returned %v, want exactly [L2]", page.Nodes)
	}
	if !page.Truncated {
		t.Error("Truncated=false when a page of 1 leaves 1 node unread")
	}

	edges, err := db.GraphListEdges(g, 0, 0)
	mustNoErr(t, "list edges", err)
	if edges.Total != 2 {
		t.Errorf("edge Total = %d, want 2", edges.Total)
	}
	var eids []string
	for _, e := range edges.Edges {
		eids = append(eids, e.ID)
	}
	wantExactly(t, "edges", eids, "x1", "x2")
	if edges.Edges[0].From != "L1" || edges.Edges[0].To != "L2" {
		t.Errorf("first edge = %+v, want L1 -> L2", edges.Edges[0])
	}
}

func TestGraphQuery(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)

	mustNoErr(t, "add q1", db.GraphAddNode(g, "q1", []string{"Person"}, map[string]any{"name": "ada", "age": 36}))
	mustNoErr(t, "add q2", db.GraphAddNode(g, "q2", []string{"Person"}, map[string]any{"name": "grace", "age": 45}))
	mustNoErr(t, "add q3", db.GraphAddNode(g, "q3", []string{"Company"}, map[string]any{"name": "acme"}))
	mustNoErr(t, "add e", db.GraphAddEdge(g, "works", "q1", "q3", "WORKS_AT", nil))

	t.Run("match by label", func(t *testing.T) {
		rows, err := db.GraphQuery(g, "MATCH (n:Person) RETURN n.name ORDER BY n.name")
		mustNoErr(t, "query", err)
		if rows == nil {
			t.Fatal("GraphQuery returned nil with a nil error")
		}
		if len(rows.Columns) != 1 || rows.Columns[0] != "n.name" {
			t.Errorf("Columns = %v, want [n.name]", rows.Columns)
		}
		if len(rows.Rows) != 2 {
			t.Fatalf("expected 2 rows, got %d (%v)", len(rows.Rows), rows.Rows)
		}
		// Label filtering really happened: "acme" is a Company, not a Person.
		if rows.Rows[0][0] != "ada" || rows.Rows[1][0] != "grace" {
			t.Errorf("rows = %v, want [[ada] [grace]]", rows.Rows)
		}
		for _, r := range rows.Rows {
			if r[0] == "acme" {
				t.Error("a Company matched (n:Person) — the label predicate was ignored")
			}
		}
		if rows.Count != 2 {
			t.Errorf("Count = %d, want 2", rows.Count)
		}
	})

	t.Run("where predicate", func(t *testing.T) {
		rows, err := db.GraphQuery(g, "MATCH (n:Person) WHERE n.age > 40 RETURN n.name")
		mustNoErr(t, "query", err)
		if len(rows.Rows) != 1 || rows.Rows[0][0] != "grace" {
			t.Errorf("rows = %v, want [[grace]]", rows.Rows)
		}
	})

	t.Run("relationship pattern", func(t *testing.T) {
		rows, err := db.GraphQuery(g, "MATCH (a:Person)-[:WORKS_AT]->(b:Company) RETURN a.name, b.name")
		mustNoErr(t, "query", err)
		if len(rows.Rows) != 1 {
			t.Fatalf("expected 1 row, got %v", rows.Rows)
		}
		if len(rows.Rows[0]) != 2 || rows.Rows[0][0] != "ada" || rows.Rows[0][1] != "acme" {
			t.Errorf("row = %v, want [ada acme]", rows.Rows[0])
		}
	})

	t.Run("write clauses are refused by name", func(t *testing.T) {
		for _, q := range []string{
			"CREATE (n:Person {name: 'mallory'})",
			"MATCH (n:Person) DELETE n",
			"MATCH (n:Person) SET n.age = 1",
		} {
			rows, err := db.GraphQuery(g, q)
			if err == nil {
				t.Errorf("nil error for a write query %q — the Cypher subset is read-only", q)
			}
			if rows != nil {
				t.Errorf("query %q returned %+v alongside an error, want nil", q, rows)
			}
		}
		// Nothing was written.
		rows, err := db.GraphQuery(g, "MATCH (n:Person) RETURN n.name")
		mustNoErr(t, "query after refusals", err)
		if len(rows.Rows) != 2 {
			t.Errorf("a refused write query changed the graph: %v", rows.Rows)
		}
	})
}

// TestGraphErrorsAreTypedAndNotSilentlyZero is the nil-error trap for the graph
// API.
func TestGraphErrorsAreTypedAndNotSilentlyZero(t *testing.T) {
	db := newClient(t)
	g := newGraph(t, db)
	mustNoErr(t, "add n", db.GraphAddNode(g, "n", nil, nil))

	t.Run("get node in a missing graph", func(t *testing.T) {
		node, found, err := db.GraphGetNode("go_no_such_graph", "n")
		if err == nil {
			t.Fatal("nil error reading a node from a graph that does not exist")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if found {
			t.Error("found=true alongside an error")
		}
		if node != nil {
			t.Errorf("returned %+v alongside an error, want nil", node)
		}
	})

	t.Run("neighbors of a missing graph", func(t *testing.T) {
		ns, err := db.GraphNeighbors("go_no_such_graph", "n", tricoredb.NeighborOptions{})
		if err == nil {
			t.Fatal("nil error listing neighbors in a graph that does not exist")
		}
		if ns != nil {
			t.Errorf("returned %v alongside an error, want nil (an empty slice reads as 'no neighbors')", ns)
		}
	})

	t.Run("degree of a missing node", func(t *testing.T) {
		d, err := db.GraphDegree(g, "no-such-node", tricoredb.DirectionBoth)
		if err == nil {
			t.Fatal("nil error taking the degree of a node that does not exist")
		}
		if d != 0 {
			t.Errorf("returned degree %d alongside an error", d)
		}
	})

	t.Run("edge with a dangling endpoint", func(t *testing.T) {
		err := db.GraphAddEdge(g, "dangling", "n", "ghost", "R", nil)
		if err == nil {
			t.Fatal("nil error adding an edge to a node that does not exist")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if _, found, _ := db.GraphGetEdge(g, "dangling"); found {
			t.Error("a refused edge was stored anyway")
		}
		if _, found, _ := db.GraphGetNode(g, "ghost"); found {
			t.Error("a refused edge created its missing endpoint")
		}
	})

	t.Run("traverse from a missing start node", func(t *testing.T) {
		tr, err := db.GraphTraverse(g, "no-such-node", tricoredb.TraverseOptions{})
		if err == nil {
			t.Fatal("nil error traversing from a node that does not exist")
		}
		if tr != nil {
			t.Errorf("returned %+v alongside an error, want nil", tr)
		}
	})

	t.Run("shortest path from a missing node", func(t *testing.T) {
		p, err := db.GraphShortestPath(g, "no-such-node", "n", tricoredb.PathOptions{})
		if err == nil {
			t.Fatal("nil error pathfinding from a node that does not exist")
		}
		if p != nil {
			t.Errorf("returned %+v alongside an error, want nil — a Found=false path "+
				"would read as 'no route', which is a different answer", p)
		}
	})

	t.Run("list nodes of a missing graph", func(t *testing.T) {
		page, err := db.GraphListNodes("go_no_such_graph", 0, 0)
		if err == nil {
			t.Fatal("nil error listing nodes of a graph that does not exist")
		}
		if page != nil {
			t.Errorf("returned %+v alongside an error, want nil", page)
		}
	})

	t.Run("query against a missing graph", func(t *testing.T) {
		rows, err := db.GraphQuery("go_no_such_graph", "MATCH (n) RETURN n")
		if err == nil {
			t.Fatal("nil error querying a graph that does not exist")
		}
		if rows != nil {
			t.Errorf("returned %+v alongside an error, want nil", rows)
		}
	})

	// A payload the server cannot deserialize gets a generic refusal, so this
	// asserts identity and non-nil-ness rather than message text.
	t.Run("malformed payload: an unrecognized direction", func(t *testing.T) {
		ns, err := db.GraphNeighbors(g, "n", tricoredb.NeighborOptions{
			Direction: tricoredb.GraphDirection("sideways"),
		})
		if err == nil {
			t.Fatal("nil error for an unrecognized direction")
		}
		if !errors.Is(err, tricoredb.ErrServer) {
			t.Errorf("errors.Is(err, ErrServer) = false for %v (%T)", err, err)
		}
		if ns != nil {
			t.Errorf("returned %v alongside an error, want nil", ns)
		}
		// The connection survives a rejected request.
		if _, _, err := db.GraphGetNode(g, "n"); err != nil {
			t.Errorf("connection unusable after a malformed request: %v", err)
		}
	})
}

func neighborNodeIDs(ns []tricoredb.GraphNeighbor) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.NodeID
	}
	return out
}

func visitIDs(vs []tricoredb.GraphVisit) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}
