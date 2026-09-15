// Command runner is the Go conformance runner for this module.
//
//	runner <host> <port> <user> <secret>   < scenario.json
//
// Reads the shared scenario on stdin, executes each step through the Go
// driver's PUBLIC TYPED API, and writes one JSON observation per line.
//
// This file makes no assertions and knows no expected values -- it is never
// told any. Every judgement lives in tricoredb-sdk-spec/conformance/scenario.js
// so that all SDKs are held to one standard. The only job here is:
//
//	canonical action + args  ->  the SDK method a user would call
//	the SDK's typed result   ->  canonical JSON
//
// The normalisation is mechanical field-copying. It must never compute,
// default, or invent a value: if the driver returns nothing, the canonical
// answer is nothing and the shared assertion fails. That is the mechanism by
// which a stubbed method is caught.
//
// Note that in a compiled SDK a *missing method* is a compile error, so an
// operation the driver does not implement fails the whole build -- the matrix
// reports that as BUILD FAILED with all 41 unproven, which is the honest
// answer.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/trinesh14/tricoredb-sdk-go"
)

type step struct {
	ID     string         `json:"id"`
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

type scenario struct {
	Steps []step `json:"steps"`
}

type observation struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Value  any    `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
}

// -- argument helpers ---------------------------------------------------------
//
// The scenario arrives as generic JSON, so numbers are float64 and objects are
// map[string]any. These convert without inventing anything: a missing argument
// yields a zero value and the resulting call fails, which is what should
// happen.

func str(a map[string]any, k string) string {
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

func intOf(a map[string]any, k string) int {
	if v, ok := a[k].(float64); ok {
		return int(v)
	}
	return 0
}

func boolOf(a map[string]any, k string) bool {
	v, _ := a[k].(bool)
	return v
}

func obj(a map[string]any, k string) map[string]any {
	if v, ok := a[k].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func strs(a map[string]any, k string) []string {
	raw, _ := a[k].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func floats(a map[string]any, k string) []float32 {
	raw, _ := a[k].([]any)
	out := make([]float32, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			out = append(out, float32(f))
		}
	}
	return out
}

func direction(a map[string]any) tricoredb.GraphDirection {
	switch str(a, "direction") {
	case "incoming":
		return tricoredb.DirectionIncoming
	case "both":
		return tricoredb.DirectionBoth
	default:
		return tricoredb.DirectionOutgoing
	}
}

func docUpdate(a map[string]any) tricoredb.DocumentUpdate {
	u := tricoredb.DocumentUpdate{}
	if set, ok := a["set"].(map[string]any); ok {
		u.Set = set
	}
	if inc, ok := a["inc"].(map[string]any); ok {
		u.Inc = inc
	}
	return u
}

// -- canonical spec -> SDK builders -------------------------------------------
//
// The scenario ships filters and pipelines as neutral descriptions. Translating
// them through the SDK's own builders (rather than hand-writing wire JSON) is
// deliberate: an SDK that cannot express And or $group fails the step instead
// of quietly sending JSON its users could not have produced.

func buildFilter(spec map[string]any) (tricoredb.DocumentFilter, error) {
	switch spec["op"] {
	case "all":
		return tricoredb.FilterAll(), nil
	case "eq":
		return tricoredb.FilterEq(str(spec, "field"), spec["value"]), nil
	case "gt":
		return tricoredb.FilterGt(str(spec, "field"), spec["value"]), nil
	case "contains":
		return tricoredb.FilterContains(str(spec, "field"), spec["value"]), nil
	case "and":
		raw, _ := spec["filters"].([]any)
		subs := make([]tricoredb.DocumentFilter, 0, len(raw))
		for _, r := range raw {
			m, _ := r.(map[string]any)
			f, err := buildFilter(m)
			if err != nil {
				return tricoredb.DocumentFilter{}, err
			}
			subs = append(subs, f)
		}
		return tricoredb.FilterAnd(subs...), nil
	default:
		return tricoredb.DocumentFilter{}, fmt.Errorf("unsupported filter op in the scenario: %v", spec["op"])
	}
}

func buildStage(spec map[string]any) (tricoredb.AggregateStage, error) {
	switch spec["stage"] {
	case "match":
		f, err := buildFilter(obj(spec, "filter"))
		if err != nil {
			return tricoredb.AggregateStage{}, err
		}
		return tricoredb.StageMatch(f), nil
	case "group":
		raw, _ := spec["accumulators"].([]any)
		accs := make([]tricoredb.GroupAccumulator, 0, len(raw))
		for _, r := range raw {
			m, _ := r.(map[string]any)
			switch m["op"] {
			case "sum":
				accs = append(accs, tricoredb.GroupAccumulator{Output: str(m, "output"), Op: tricoredb.AccSum(str(m, "field"))})
			case "count":
				accs = append(accs, tricoredb.GroupAccumulator{Output: str(m, "output"), Op: tricoredb.AccCount()})
			default:
				return tricoredb.AggregateStage{}, fmt.Errorf("unsupported accumulator: %v", m["op"])
			}
		}
		return tricoredb.StageGroup(tricoredb.GroupByField(str(obj(spec, "by"), "field")), accs...), nil
	case "sort":
		raw, _ := spec["keys"].([]any)
		keys := make([]tricoredb.SortKey, 0, len(raw))
		for _, r := range raw {
			m, _ := r.(map[string]any)
			desc, _ := m["descending"].(bool)
			keys = append(keys, tricoredb.SortKey{Field: str(m, "field"), Descending: desc})
		}
		return tricoredb.StageSort(keys...), nil
	case "count":
		return tricoredb.StageCount(str(spec, "field")), nil
	default:
		return tricoredb.AggregateStage{}, fmt.Errorf("unsupported aggregate stage: %v", spec["stage"])
	}
}

// -- actions ------------------------------------------------------------------

type actionFn func(c *tricoredb.Client, a map[string]any) (any, error)

var actions map[string]actionFn

// ctxActionFn is an action whose input includes the results of earlier steps.
type ctxActionFn func(c *tricoredb.Client, a map[string]any, results map[string]any) (any, error)

var ctxActions map[string]ctxActionFn

func init() {
	actions = map[string]actionFn{
		// ---- document ----
		"doc.createCollection": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentCreateCollection(str(a, "collection"))
		},
		"doc.dropCollection": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentDropCollection(str(a, "collection"))
		},
		"doc.listCollections": func(c *tricoredb.Client, a map[string]any) (any, error) {
			names, err := c.DocumentListCollections()
			return map[string]any{"names": names}, err
		},
		"doc.insert": func(c *tricoredb.Client, a map[string]any) (any, error) {
			id, err := c.DocumentInsertWithID(str(a, "collection"), str(a, "id"), obj(a, "document"))
			return map[string]any{"id": id}, err
		},
		"doc.get": func(c *tricoredb.Client, a map[string]any) (any, error) {
			doc, found, err := c.DocumentGet(str(a, "collection"), str(a, "id"))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": true, "doc": doc}, nil
		},
		"doc.find": func(c *tricoredb.Client, a map[string]any) (any, error) {
			f, err := buildFilter(obj(a, "filter"))
			if err != nil {
				return nil, err
			}
			var docs []tricoredb.Document
			if _, ok := a["limit"]; ok {
				docs, err = c.DocumentFindLimit(str(a, "collection"), f, intOf(a, "limit"))
			} else {
				docs, err = c.DocumentFind(str(a, "collection"), f)
			}
			return map[string]any{"docs": docs}, err
		},
		"doc.update": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentUpdate(str(a, "collection"), str(a, "id"), obj(a, "set"))
		},
		"doc.updateOne": func(c *tricoredb.Client, a map[string]any) (any, error) {
			if boolOf(a, "upsert") {
				_, err := c.DocumentUpsertOne(str(a, "collection"), str(a, "id"), docUpdate(a))
				return map[string]any{}, err
			}
			return map[string]any{}, c.DocumentUpdateOne(str(a, "collection"), str(a, "id"), docUpdate(a))
		},
		"doc.updateMany": func(c *tricoredb.Client, a map[string]any) (any, error) {
			f, err := buildFilter(obj(a, "filter"))
			if err != nil {
				return nil, err
			}
			r, err := c.DocumentUpdateMany(str(a, "collection"), f, docUpdate(a))
			return map[string]any{"matched": r.Matched, "modified": r.Modified}, err
		},
		"doc.delete": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentDelete(str(a, "collection"), str(a, "id"))
		},
		"doc.createIndex": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentCreateIndex(str(a, "collection"), str(a, "indexName"), str(a, "field"), boolOf(a, "unique"))
		},
		"doc.dropIndex": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.DocumentDropIndex(str(a, "collection"), str(a, "indexName"))
		},
		"doc.listIndexes": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ixs, err := c.DocumentListIndexes(str(a, "collection"))
			out := make([]map[string]any, 0, len(ixs))
			for _, i := range ixs {
				out = append(out, map[string]any{"name": i.Name, "field": i.Field})
			}
			return map[string]any{"indexes": out}, err
		},
		"doc.analyze": func(c *tricoredb.Client, a map[string]any) (any, error) {
			s, err := c.DocumentAnalyze(str(a, "collection"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"document_count": s.DocumentCount}, nil
		},
		"doc.aggregate": func(c *tricoredb.Client, a map[string]any) (any, error) {
			raw, _ := a["pipeline"].([]any)
			stages := make([]tricoredb.AggregateStage, 0, len(raw))
			for _, r := range raw {
				m, _ := r.(map[string]any)
				s, err := buildStage(m)
				if err != nil {
					return nil, err
				}
				stages = append(stages, s)
			}
			docs, err := c.DocumentAggregate(str(a, "collection"), stages...)
			return map[string]any{"docs": docs}, err
		},

		// ---- vector ----
		"vec.createCollection": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.VectorCreateCollection(str(a, "collection"), intOf(a, "dimension"), tricoredb.VectorMetric(str(a, "metric")))
		},
		"vec.dropCollection": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.VectorDropCollection(str(a, "collection"))
		},
		"vec.listCollections": func(c *tricoredb.Client, a map[string]any) (any, error) {
			cols, err := c.VectorListCollections()
			names := make([]string, 0, len(cols))
			for _, s := range cols {
				names = append(names, s.Name)
			}
			return map[string]any{"names": names}, err
		},
		"vec.upsert": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.VectorUpsert(str(a, "collection"), str(a, "id"), floats(a, "vector"), obj(a, "metadata"))
		},
		"vec.get": func(c *tricoredb.Client, a map[string]any) (any, error) {
			item, found, err := c.VectorGet(str(a, "collection"), str(a, "id"))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": true, "id": item.ID, "vector": item.Vector, "metadata": item.Metadata}, nil
		},
		"vec.delete": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.VectorDelete(str(a, "collection"), str(a, "id"))
		},
		"vec.search": func(c *tricoredb.Client, a map[string]any) (any, error) {
			var res *tricoredb.VectorSearchResult
			var err error
			if f, ok := a["filter"].(map[string]any); ok {
				res, err = c.VectorSearchFiltered(str(a, "collection"), floats(a, "vector"), intOf(a, "topK"), f)
			} else {
				res, err = c.VectorSearch(str(a, "collection"), floats(a, "vector"), intOf(a, "topK"))
			}
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(res.Results))
			scores := make([]float32, 0, len(res.Results))
			for _, h := range res.Results {
				ids = append(ids, h.ID)
				scores = append(scores, h.Score)
			}
			return map[string]any{"ids": ids, "scores": scores}, nil
		},
		"vec.describeCollection": func(c *tricoredb.Client, a map[string]any) (any, error) {
			info, err := c.VectorDescribeCollection(str(a, "collection"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"dimension": info.Dimension, "metric": string(info.Metric), "count": info.Count}, nil
		},
		"vec.listVectors": func(c *tricoredb.Client, a map[string]any) (any, error) {
			page, err := c.VectorListVectors(str(a, "collection"), 0, 0)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(page.Vectors))
			for _, v := range page.Vectors {
				ids = append(ids, v.ID)
			}
			return map[string]any{"ids": ids, "total": page.Total}, nil
		},

		// ---- graph ----
		"graph.create": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.GraphCreate(str(a, "graph"))
		},
		"graph.drop": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.GraphDrop(str(a, "graph"))
		},
		"graph.listGraphs": func(c *tricoredb.Client, a map[string]any) (any, error) {
			names, err := c.GraphList()
			return map[string]any{"names": names}, err
		},
		"graph.addNode": func(c *tricoredb.Client, a map[string]any) (any, error) {
			// labels goes out as [] and never as nil: the server refuses null.
			return map[string]any{}, c.GraphAddNode(str(a, "graph"), str(a, "id"), strs(a, "labels"), obj(a, "properties"))
		},
		"graph.getNode": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, found, err := c.GraphGetNode(str(a, "graph"), str(a, "id"))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": true, "id": n.ID, "labels": n.Labels, "properties": n.Properties}, nil
		},
		"graph.deleteNode": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.GraphDeleteNode(str(a, "graph"), str(a, "id"))
		},
		"graph.addEdge": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.GraphAddEdge(str(a, "graph"), str(a, "id"), str(a, "from"), str(a, "to"), str(a, "label"), obj(a, "properties"))
		},
		"graph.getEdge": func(c *tricoredb.Client, a map[string]any) (any, error) {
			e, found, err := c.GraphGetEdge(str(a, "graph"), str(a, "id"))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": true, "id": e.ID, "from": e.From, "to": e.To, "label": e.Label, "properties": e.Properties}, nil
		},
		"graph.deleteEdge": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.GraphDeleteEdge(str(a, "graph"), str(a, "id"))
		},
		"graph.neighbors": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ns, err := c.GraphNeighbors(str(a, "graph"), str(a, "nodeId"), tricoredb.NeighborOptions{
				Direction: direction(a),
				Label:     str(a, "label"),
			})
			if err != nil {
				return nil, err
			}
			nodeIDs := make([]string, 0, len(ns))
			edgeIDs := make([]string, 0, len(ns))
			for _, n := range ns {
				nodeIDs = append(nodeIDs, n.NodeID)
				edgeIDs = append(edgeIDs, n.EdgeID)
			}
			return map[string]any{"nodeIds": nodeIDs, "edgeIds": edgeIDs}, nil
		},
		"graph.degree": func(c *tricoredb.Client, a map[string]any) (any, error) {
			d, err := c.GraphDegree(str(a, "graph"), str(a, "nodeId"), direction(a))
			return map[string]any{"degree": d}, err
		},
		"graph.traverse": func(c *tricoredb.Client, a map[string]any) (any, error) {
			t, err := c.GraphTraverse(str(a, "graph"), str(a, "start"), tricoredb.TraverseOptions{
				Direction: direction(a),
				MaxDepth:  intOf(a, "maxDepth"),
			})
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(t.Nodes))
			depths := map[string]int{}
			for _, v := range t.Nodes {
				ids = append(ids, v.ID)
				depths[v.ID] = v.Depth
			}
			return map[string]any{"ids": ids, "depths": depths}, nil
		},
		"graph.shortestPath": func(c *tricoredb.Client, a map[string]any) (any, error) {
			p, err := c.GraphShortestPath(str(a, "graph"), str(a, "from"), str(a, "to"), tricoredb.PathOptions{})
			if err != nil {
				return nil, err
			}
			return map[string]any{"found": p.Found, "hops": p.Hops, "nodePath": p.NodePath, "edgePath": p.EdgePath}, nil
		},
		"graph.weightedShortestPath": func(c *tricoredb.Client, a map[string]any) (any, error) {
			p, err := c.GraphWeightedShortestPath(str(a, "graph"), str(a, "from"), str(a, "to"), tricoredb.WeightedPathOptions{
				WeightProperty: str(a, "weightProperty"),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"found": p.Found, "totalCost": p.TotalCost, "nodePath": p.NodePath, "edgePath": p.EdgePath}, nil
		},
		"graph.listNodes": func(c *tricoredb.Client, a map[string]any) (any, error) {
			page, err := c.GraphListNodes(str(a, "graph"), 0, 0)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(page.Nodes))
			labels := map[string][]string{}
			for _, n := range page.Nodes {
				ids = append(ids, n.ID)
				labels[n.ID] = n.Labels
			}
			return map[string]any{"ids": ids, "labels": labels, "total": page.Total}, nil
		},
		"graph.listEdges": func(c *tricoredb.Client, a map[string]any) (any, error) {
			page, err := c.GraphListEdges(str(a, "graph"), 0, 0)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(page.Edges))
			labels := map[string]string{}
			for _, e := range page.Edges {
				ids = append(ids, e.ID)
				labels[e.ID] = e.Label
			}
			return map[string]any{"ids": ids, "labels": labels, "total": page.Total}, nil
		},
		"graph.query": func(c *tricoredb.Client, a map[string]any) (any, error) {
			rows, err := c.GraphQuery(str(a, "graph"), str(a, "cypher"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"columns": rows.Columns, "rows": rows.Rows}, nil
		},
		// ---- sql ----
		//
		// The Query/Exec split is a security boundary, not a convenience: Query
		// is a read and may only run SELECT. Both get their own action so the
		// scenario can prove the refusal as well as the success.
		"sql.execute": func(c *tricoredb.Client, a map[string]any) (any, error) {
			resp, err := c.Execute(str(a, "sql"))
			if err != nil {
				return nil, err
			}
			var payload struct {
				RowsAffected *int64 `json:"rows_affected"`
			}
			// Not every Exec answers with a row count (DDL and transaction
			// scripts do not), so an absent field stays null rather than 0.
			_ = json.Unmarshal(resp.DataRaw, &payload)
			if payload.RowsAffected == nil {
				return map[string]any{"rowsAffected": nil}, nil
			}
			return map[string]any{"rowsAffected": *payload.RowsAffected}, nil
		},
		"sql.query": func(c *tricoredb.Client, a map[string]any) (any, error) {
			rows, err := c.Query(str(a, "sql"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"columns": rows.Columns, "rows": rows.Rows}, nil
		},

		// ---- cache ----
		//
		// Values cross this boundary as text, but every call below goes through
		// the driver's BYTE-oriented API. An SDK that could only speak strings
		// would be unable to store what the server actually stores.
		"cache.ping": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.CachePing()
		},
		"cache.set": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.CacheSetTTL(str(a, "namespace"), str(a, "key"),
				[]byte(str(a, "value")), int64(intOf(a, "ttlMs")))
		},
		"cache.get": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, found, err := c.CacheGet(str(a, "namespace"), str(a, "key"))
			return foundValue(v, found), err
		},
		"cache.delete": func(c *tricoredb.Client, a map[string]any) (any, error) {
			existed, err := c.CacheDelete(str(a, "namespace"), str(a, "key"))
			return map[string]any{"deleted": existed}, err
		},
		"cache.exists": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ok, err := c.CacheExists(str(a, "namespace"), str(a, "key"))
			return map[string]any{"exists": ok}, err
		},
		"cache.ttl": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ttl, has, err := c.CacheTTL(str(a, "namespace"), str(a, "key"))
			if err != nil {
				return nil, err
			}
			if !has {
				return map[string]any{"hasTtl": false}, nil
			}
			return map[string]any{"hasTtl": true, "ttlMs": ttl}, nil
		},
		"cache.clearNamespace": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheClearNamespace(str(a, "namespace"))
			return map[string]any{"cleared": n}, err
		},
		"cache.incr": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, err := c.CacheIncr(str(a, "namespace"), str(a, "key"), int64(intOf(a, "by")))
			return map[string]any{"value": v}, err
		},
		"cache.expire": func(c *tricoredb.Client, a map[string]any) (any, error) {
			updated, err := c.CacheExpire(str(a, "namespace"), str(a, "key"), int64(intOf(a, "ttlMs")))
			return map[string]any{"updated": updated}, err
		},
		"cache.persist": func(c *tricoredb.Client, a map[string]any) (any, error) {
			p, err := c.CachePersist(str(a, "namespace"), str(a, "key"))
			return map[string]any{"persisted": p}, err
		},
		"cache.setNx": func(c *tricoredb.Client, a map[string]any) (any, error) {
			set, err := c.CacheSetNx(str(a, "namespace"), str(a, "key"),
				[]byte(str(a, "value")), int64(intOf(a, "ttlMs")))
			return map[string]any{"set": set}, err
		},
		"cache.keys": func(c *tricoredb.Client, a map[string]any) (any, error) {
			keys, err := c.CacheKeys(str(a, "namespace"), str(a, "pattern"), 0)
			if err != nil {
				return nil, err
			}
			names := make([]string, len(keys))
			for i, k := range keys {
				names[i] = k.Key
			}
			return map[string]any{"keys": names}, nil
		},

		"cache.lPush": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheLPush(str(a, "namespace"), str(a, "key"), byteSlices(a, "values")...)
			return map[string]any{"length": n}, err
		},
		"cache.rPush": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheRPush(str(a, "namespace"), str(a, "key"), byteSlices(a, "values")...)
			return map[string]any{"length": n}, err
		},
		"cache.lPop": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, found, err := c.CacheLPop(str(a, "namespace"), str(a, "key"))
			return foundValue(v, found), err
		},
		"cache.rPop": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, found, err := c.CacheRPop(str(a, "namespace"), str(a, "key"))
			return foundValue(v, found), err
		},
		"cache.lRange": func(c *tricoredb.Client, a map[string]any) (any, error) {
			vals, err := c.CacheLRange(str(a, "namespace"), str(a, "key"),
				int64(intOf(a, "start")), int64(intOf(a, "stop")))
			return map[string]any{"values": texts(vals)}, err
		},
		"cache.lLen": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheLLen(str(a, "namespace"), str(a, "key"))
			return map[string]any{"length": n}, err
		},
		"cache.lIndex": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, found, err := c.CacheLIndex(str(a, "namespace"), str(a, "key"), int64(intOf(a, "index")))
			return foundValue(v, found), err
		},

		"cache.sAdd": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheSAdd(str(a, "namespace"), str(a, "key"), byteSlices(a, "members")...)
			return map[string]any{"added": n}, err
		},
		"cache.sRem": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheSRem(str(a, "namespace"), str(a, "key"), byteSlices(a, "members")...)
			return map[string]any{"removed": n}, err
		},
		"cache.sIsMember": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ok, err := c.CacheSIsMember(str(a, "namespace"), str(a, "key"), []byte(str(a, "member")))
			return map[string]any{"isMember": ok}, err
		},
		"cache.sCard": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheSCard(str(a, "namespace"), str(a, "key"))
			return map[string]any{"cardinality": n}, err
		},
		"cache.sMembers": func(c *tricoredb.Client, a map[string]any) (any, error) {
			m, err := c.CacheSMembers(str(a, "namespace"), str(a, "key"))
			return map[string]any{"members": texts(m)}, err
		},

		"cache.hSet": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheHSet(str(a, "namespace"), str(a, "key"), cachePairs(a, "entries"))
			return map[string]any{"created": n}, err
		},
		"cache.hGet": func(c *tricoredb.Client, a map[string]any) (any, error) {
			v, found, err := c.CacheHGet(str(a, "namespace"), str(a, "key"), []byte(str(a, "field")))
			return foundValue(v, found), err
		},
		"cache.hDel": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheHDel(str(a, "namespace"), str(a, "key"), byteSlices(a, "fields")...)
			return map[string]any{"deleted": n}, err
		},
		"cache.hGetAll": func(c *tricoredb.Client, a map[string]any) (any, error) {
			pairs, err := c.CacheHGetAll(str(a, "namespace"), str(a, "key"))
			if err != nil {
				return nil, err
			}
			out := make([][]string, len(pairs))
			for i, p := range pairs {
				out[i] = []string{string(p.Field), string(p.Value)}
			}
			return map[string]any{"entries": out}, nil
		},
		"cache.hExists": func(c *tricoredb.Client, a map[string]any) (any, error) {
			ok, err := c.CacheHExists(str(a, "namespace"), str(a, "key"), []byte(str(a, "field")))
			return map[string]any{"exists": ok}, err
		},
		"cache.hLen": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheHLen(str(a, "namespace"), str(a, "key"))
			return map[string]any{"length": n}, err
		},

		"cache.xAdd": func(c *tricoredb.Client, a map[string]any) (any, error) {
			id, err := c.CacheXAdd(str(a, "namespace"), str(a, "key"), cachePairs(a, "fields"), "")
			return map[string]any{"id": id}, err
		},
		"cache.xLen": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheXLen(str(a, "namespace"), str(a, "key"))
			return map[string]any{"length": n}, err
		},
		"cache.xRange": func(c *tricoredb.Client, a map[string]any) (any, error) {
			entries, err := c.CacheXRange(str(a, "namespace"), str(a, "key"),
				str(a, "start"), str(a, "end"), 0)
			return map[string]any{"entries": streamEntries(entries)}, err
		},
		"cache.xTrim": func(c *tricoredb.Client, a map[string]any) (any, error) {
			n, err := c.CacheXTrim(str(a, "namespace"), str(a, "key"), intOf(a, "maxLen"))
			return map[string]any{"trimmed": n}, err
		},
		"cache.xGroup": func(c *tricoredb.Client, a map[string]any) (any, error) {
			// Consumer groups are refused by name in V1. The driver exposes no
			// typed helper for an operation that can only fail, so this goes
			// through the raw request path -- which is itself the honest answer
			// to "can this SDK reach the operation at all".
			_, err := c.Request(map[string]any{"Cache": map[string]any{"XGroup": map[string]any{
				"namespace": str(a, "namespace"), "key": str(a, "key"), "command": str(a, "command"),
			}}})
			return map[string]any{}, err
		},

		// ---- llm ----
		"llm.schema": func(c *tricoredb.Client, a map[string]any) (any, error) {
			s, err := c.LlmSchema(tricoredb.OutputFormat(str(a, "format")), nil)
			return map[string]any{"rendered": s}, err
		},
		"llm.context": func(c *tricoredb.Client, a map[string]any) (any, error) {
			raw, _ := a["sources"].([]any)
			sources := make([]tricoredb.LlmSource, 0, len(raw))
			for _, s := range raw {
				spec, _ := s.(map[string]any)
				if q, ok := spec["sql"].(string); ok {
					sources = append(sources, tricoredb.SQLSource(q))
				} else {
					sources = append(sources, tricoredb.DocumentFindSource(
						str(spec, "collection"), tricoredb.FilterAll(), 0))
				}
			}
			out, err := c.LlmContext(sources, tricoredb.OutputFormat(str(a, "format")), nil)
			return map[string]any{"rendered": out}, err
		},

		// ---- admin ----
		"admin.ping": func(c *tricoredb.Client, a map[string]any) (any, error) {
			return map[string]any{}, c.AdminPing()
		},
		"admin.status": func(c *tricoredb.Client, a map[string]any) (any, error) {
			st, err := c.AdminStatus()
			return map[string]any{"status": st}, err
		},
	}

	// Two actions take an argument that IS an earlier step's answer (a stream
	// cursor, an id to delete). They live in their own table so the ordinary
	// 180 handlers keep the two-parameter signature.
	ctxActions = map[string]ctxActionFn{
		"cache.xRead": func(c *tricoredb.Client, a map[string]any, results map[string]any) (any, error) {
			entries, err := c.CacheXRead(str(a, "namespace"), str(a, "key"),
				idFromStep(results, str(a, "afterStep")), 0)
			return map[string]any{"entries": streamEntries(entries)}, err
		},
		"cache.xDel": func(c *tricoredb.Client, a map[string]any, results map[string]any) (any, error) {
			var ids []string
			for _, step := range strs(a, "idsFromSteps") {
				ids = append(ids, idFromStep(results, step))
			}
			n, err := c.CacheXDel(str(a, "namespace"), str(a, "key"), ids...)
			return map[string]any{"deleted": n}, err
		},
	}
}

// -- cache encoding helpers ---------------------------------------------------
//
// Mechanical translation only: text from the scenario becomes bytes for the
// driver and back again. No defaults, no computed values.

func byteSlices(a map[string]any, k string) [][]byte {
	raw, _ := a[k].([]any)
	out := make([][]byte, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, []byte(s))
		}
	}
	return out
}

func cachePairs(a map[string]any, k string) []tricoredb.CachePair {
	raw, _ := a[k].([]any)
	out := make([]tricoredb.CachePair, 0, len(raw))
	for _, item := range raw {
		pair, _ := item.([]any)
		if len(pair) != 2 {
			continue
		}
		f, _ := pair[0].(string)
		v, _ := pair[1].(string)
		out = append(out, tricoredb.CachePair{Field: []byte(f), Value: []byte(v)})
	}
	return out
}

func texts(values [][]byte) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func streamEntries(entries []tricoredb.StreamEntry) []map[string]any {
	out := make([]map[string]any, len(entries))
	for i, e := range entries {
		fields := make([][]string, len(e.Fields))
		for j, f := range e.Fields {
			fields[j] = []string{string(f.Field), string(f.Value)}
		}
		out[i] = map[string]any{"id": e.ID, "fields": fields}
	}
	return out
}

// idFromStep reads the `id` an earlier step returned. A missing step yields ""
// and the call then fails, which is the correct outcome for a scenario that
// names a step that did not run.
func idFromStep(results map[string]any, step string) string {
	m, _ := results[step].(map[string]any)
	id, _ := m["id"].(string)
	return id
}

// foundValue is the canonical shape for "a value, or a miss".
func foundValue(v []byte, found bool) map[string]any {
	if !found {
		return map[string]any{"found": false}
	}
	return map[string]any{"found": true, "value": string(v)}
}

// -- driver -------------------------------------------------------------------

func emit(o observation) {
	b, err := json.Marshal(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "go runner: cannot encode observation for %s: %v\n", o.ID, err)
		return
	}
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: conf-runner <host> <port> <user> <secret> < scenario.json")
		os.Exit(2)
	}
	host := os.Args[1]
	port, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "go runner: bad port %q: %v\n", os.Args[2], err)
		os.Exit(2)
	}

	var sc scenario
	if err := json.NewDecoder(os.Stdin).Decode(&sc); err != nil {
		fmt.Fprintf(os.Stderr, "go runner: cannot read the scenario: %v\n", err)
		os.Exit(2)
	}

	client, err := tricoredb.Connect(context.Background(), tricoredb.Options{
		Host: host, Port: port, User: os.Args[3], Secret: os.Args[4],
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "go runner: cannot connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// Values from earlier steps, for the few actions whose argument IS an
	// earlier answer. Resolving it here is mechanical lookup, not judgement:
	// the scenario names the step it wants.
	results := map[string]any{}

	for _, s := range sc.Steps {
		var (
			value any
			err   error
		)
		if fn, ok := actions[s.Action]; ok {
			value, err = fn(client, s.Args)
		} else if fn, ok := ctxActions[s.Action]; ok {
			value, err = fn(client, s.Args, results)
		} else {
			emit(observation{ID: s.ID, Status: "unsupported", Error: "no Go SDK method for action " + s.Action})
			continue
		}
		if err != nil {
			emit(observation{ID: s.ID, Status: "error", Error: fmt.Sprintf("%T: %v", err, err)})
			continue
		}
		results[s.ID] = value
		emit(observation{ID: s.ID, Status: "ok", Value: value})
	}
}
