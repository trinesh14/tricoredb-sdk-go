package tricoredb

import "fmt"

// The LLM family assembles read-only context bundles, and the admin family
// exposes two privileged reads. No LLM operation ever writes.

// OutputFormat is how the server should render an export.
type OutputFormat string

const (
	// FormatNative is TriCoreDB's own representation.
	FormatNative OutputFormat = "native"
	// FormatJSON is standard JSON.
	FormatJSON OutputFormat = "json"
	// FormatTOON is the token-oriented rendering for LLM context windows.
	FormatTOON OutputFormat = "toon"
	// FormatMarkdown is human-readable Markdown.
	FormatMarkdown OutputFormat = "markdown"
)

// LlmOptions bounds and redacts an export.
//
// RedactSensitive defaults to false in the zero value, which is the opposite of
// the server's default — so LlmContext and LlmSchema take a *LlmOptions and
// substitute DefaultLlmOptions when it is nil. A zero-value struct passed by
// accident should not silently turn redaction off.
type LlmOptions struct {
	// MaxRows caps rows included; 0 leaves the cap to the server.
	MaxRows int
	// RedactSensitive redacts sensitive fields before export.
	RedactSensitive bool
	// IncludeSchema includes type information alongside the data.
	IncludeSchema bool
}

// DefaultLlmOptions matches the server's defaults: redact, no schema, no cap.
func DefaultLlmOptions() LlmOptions {
	return LlmOptions{RedactSensitive: true}
}

func (o LlmOptions) wire() map[string]any {
	var maxRows any
	if o.MaxRows > 0 {
		maxRows = o.MaxRows
	}
	return map[string]any{
		"max_rows":         maxRows,
		"redact_sensitive": o.RedactSensitive,
		"include_schema":   o.IncludeSchema,
	}
}

// LlmSource is one read-only source contributing to a context bundle.
type LlmSource struct {
	body any
}

// SQLSource is a SQL SELECT. The caller needs SQL read permission.
func SQLSource(query string) LlmSource {
	return LlmSource{body: map[string]any{"Sql": map[string]any{"query": query}}}
}

// DocumentFindSource is a document find. The caller needs document read
// permission. A zero DocumentFilter means "match everything".
func DocumentFindSource(collection string, filter DocumentFilter, limit int) LlmSource {
	f := filter.body
	if f == nil {
		f = FilterAll().body
	}
	var lim any
	if limit > 0 {
		lim = limit
	}
	return LlmSource{body: map[string]any{"DocumentFind": map[string]any{
		"collection": collection,
		"filter":     f,
		"limit":      lim,
	}}}
}

// LlmContext assembles a context bundle from one or more read-only sources.
//
// opts may be nil, which uses DefaultLlmOptions. The returned string is the
// rendered bundle: text for TOON/Markdown, JSON otherwise.
func (c *Client) LlmContext(sources []LlmSource, format OutputFormat, opts *LlmOptions) (string, error) {
	if len(sources) == 0 {
		return "", &ArgumentError{Message: "a context bundle needs at least one source"}
	}
	bodies := make([]any, len(sources))
	for i, s := range sources {
		if s.body == nil {
			return "", &ArgumentError{Message: "an LLM source must be built with SQLSource or DocumentFindSource"}
		}
		bodies[i] = s.body
	}
	o := DefaultLlmOptions()
	if opts != nil {
		o = *opts
	}
	resp, err := c.request(map[string]any{"Llm": map[string]any{"Context": map[string]any{
		"sources": bodies,
		"format":  string(format),
		"options": o.wire(),
	}}})
	if err != nil {
		return "", err
	}
	return renderedExport(resp)
}

// LlmSchema exports the schema catalog: SQL tables plus document collections.
func (c *Client) LlmSchema(format OutputFormat, opts *LlmOptions) (string, error) {
	o := DefaultLlmOptions()
	if opts != nil {
		o = *opts
	}
	resp, err := c.request(map[string]any{"Llm": map[string]any{"Schema": map[string]any{
		"format":  string(format),
		"options": o.wire(),
	}}})
	if err != nil {
		return "", err
	}
	return renderedExport(resp)
}

// renderedExport unwraps whichever payload the requested format produced.
func renderedExport(resp *Response) (string, error) {
	switch resp.DataKind {
	case "Toon", "Message":
		var s string
		if err := decodeInto(resp.DataRaw, &s); err != nil {
			return "", &ProtocolError{Message: "malformed export payload: " + err.Error()}
		}
		return s, nil
	case "Json":
		return string(resp.DataRaw), nil
	default:
		return "", &ProtocolError{
			Message: fmt.Sprintf("expected a rendered export, got %s", kindOrRaw(resp)),
		}
	}
}

// -- admin -------------------------------------------------------------------
//
// Both require the Admin permission *and* the `cluster` module, so an
// unprivileged or misconfigured caller gets an error rather than silently empty
// data.

// AdminPing round-trips a request through the full pipeline.
//
// Distinct from Ping, which never reaches a module. This one proves auth,
// routing and dispatch are working — what a readiness check actually wants.
func (c *Client) AdminPing() error {
	_, err := c.request(map[string]any{"Admin": "Ping"})
	return err
}

// AdminStatus is the server status as reported by the cluster core.
//
// Stub cores answer with a plain message rather than structured JSON, which is
// returned under the "message" key rather than raised as an error, so a status
// call never hard-fails on a healthy single node.
func (c *Client) AdminStatus() (map[string]any, error) {
	resp, err := c.request(map[string]any{"Admin": "Status"})
	if err != nil {
		return nil, err
	}
	if resp.DataKind == "Message" {
		var s string
		if err := decodeInto(resp.DataRaw, &s); err != nil {
			return nil, &ProtocolError{Message: "malformed status message: " + err.Error()}
		}
		return map[string]any{"message": s}, nil
	}
	var out map[string]any
	if err := decodeJSON(resp, "admin status", &out); err != nil {
		return nil, err
	}
	return out, nil
}
