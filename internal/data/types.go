package data

type FunctionInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	PkgPath      string   `json:"pkgPath"`
	PkgName      string   `json:"pkgName"`
	Signature    string   `json:"signature"`
	Doc          string   `json:"doc"`
	File         string   `json:"file"`
	Line         int      `json:"line"`
	CallerIDs    []string `json:"callerIds"`
	CalleeIDs    []string `json:"calleeIds"`
	ReceiverType string   `json:"receiverType"` // short type name, e.g. "Dog" or "*Dog"; empty for free funcs
	IsInterface  bool     `json:"isInterface"`  // true if receiver is an interface type
}

type PackageInfo struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Functions []FunctionInfo `json:"functions"`
}

type Edge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	IsGoroutine bool   `json:"isGoroutine"`
}

// ImplementEdge connects an interface method to the concrete method that satisfies it.
type ImplementEdge struct {
	InterfaceMethod string `json:"interfaceMethod"`
	ConcreteMethod  string `json:"concreteMethod"`
}

type GraphData struct {
	Packages       []PackageInfo   `json:"packages"`
	Edges          []Edge          `json:"edges"`
	ImplementEdges []ImplementEdge `json:"implementEdges"`
	FocusPkg       string          `json:"focusPkg"`
	ModulePath     string          `json:"modulePath"`
}
