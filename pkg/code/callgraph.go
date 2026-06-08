package code

// CallEdge is a caller→callee relationship. Mirrors the shape consumed by
// pkg/tooling.GetCallGraph.
type CallEdge struct {
	CallerID string
	CalleeID string
	CallType string // "static", "trait", "closure"
	File     string
	Line     int
}

// ImplementsEdge is a type-implements-trait relationship.
type ImplementsEdge struct {
	TypeID      string
	InterfaceID string
}

// CallGraphResult is the result of a call-graph computation.
type CallGraphResult struct {
	Calls      []CallEdge
	Implements []ImplementsEdge
	Error      string
}

// ComputeCallGraph is not yet implemented for Rust — core has no Rust
// static-analysis backend (the Go agent uses go/packages + VTA). It returns an
// empty result flagged with an explanatory error so the Tooling layer degrades
// gracefully rather than failing. Restore once a rust-analyzer / syn-based
// analyzer lands in core.
func (c *Code) ComputeCallGraph(_ string) *CallGraphResult {
	return &CallGraphResult{
		Error: "call graph analysis is not yet supported for Rust services",
	}
}
