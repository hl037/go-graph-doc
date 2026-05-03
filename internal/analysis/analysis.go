package analysis

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/hl037/go-graph-doc/internal/data"
)

type Config struct {
	Patterns      []string
	Dir           string
	Focus         string
	ExcludeStd    bool
	ExcludeVendor bool
	Tests         bool
	modulePath    string
}

func Analyze(cfg Config) (*data.GraphData, error) {
	fset := token.NewFileSet()
	loadCfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedTypesSizes |
			packages.NeedModule,
		Fset:  fset,
		Tests: cfg.Tests,
		Dir:   cfg.Dir,
	}

	pkgs, err := packages.Load(loadCfg, cfg.Patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	packages.Visit(pkgs, func(pkg *packages.Package) bool {
		if pkg.Module != nil && cfg.modulePath == "" {
			cfg.modulePath = pkg.Module.Path
		}
		if pkg.Module != nil && pkg.Module.Main {
			cfg.modulePath = pkg.Module.Path
			return false
		}
		return true
	}, nil)

	if cfg.Focus == "" {
		var rootPaths []string
		for _, pkg := range pkgs {
			if pkg.PkgPath != "" && !strings.Contains(pkg.PkgPath, "_test") {
				rootPaths = append(rootPaths, pkg.PkgPath)
			}
		}
		if prefix := commonPathPrefix(rootPaths); prefix != "" && prefix != cfg.modulePath {
			cfg.Focus = prefix
		}
	}

	var errs []string
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		for _, e := range pkg.Errors {
			errs = append(errs, fmt.Sprintf("%s: %v", pkg.PkgPath, e))
		}
	})
	if len(errs) > 0 {
		fmt.Printf("warning: %d package error(s):\n", len(errs))
		for _, e := range errs {
			fmt.Printf("  %s\n", e)
		}
	}

	return buildGraphData(pkgs, fset, cfg)
}

// staticFuncID produces a canonical ID for a types.Func matching the format
// used throughout: "pkg.Func" for free functions, "(pkg.T).M" or "(*pkg.T).M" for methods.
func staticFuncID(fn *types.Func) string {
	pkg := fn.Pkg()
	if pkg == nil {
		return fn.FullName()
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return fn.FullName()
	}
	recv := sig.Recv()
	if recv == nil {
		return pkg.Path() + "." + fn.Name()
	}
	t := recv.Type()
	ptr := false
	if pt, ok2 := t.(*types.Pointer); ok2 {
		t = pt.Elem()
		ptr = true
	}
	if named, ok2 := t.(*types.Named); ok2 {
		typePkg := named.Obj().Pkg()
		pkgPath := pkg.Path()
		if typePkg != nil {
			pkgPath = typePkg.Path()
		}
		if ptr {
			return fmt.Sprintf("(*%s.%s).%s", pkgPath, named.Obj().Name(), fn.Name())
		}
		return fmt.Sprintf("(%s.%s).%s", pkgPath, named.Obj().Name(), fn.Name())
	}
	return fn.FullName()
}

func buildGraphData(pkgs []*packages.Package, fset *token.FileSet, cfg Config) (*data.GraphData, error) {
	nodeMap := make(map[string]*data.FunctionInfo)
	pkgMap := make(map[string]*data.PackageInfo)

	addFunc := func(fn *types.Func, pkg *packages.Package) {
		if fn == nil || fn.Pkg() == nil {
			return
		}
		pkgPath := fn.Pkg().Path()
		if !shouldInclude(pkgPath, cfg) {
			return
		}
		// Skip synthetic init (no position)
		if fn.Name() == "init" && !fn.Pos().IsValid() {
			return
		}
		id := staticFuncID(fn)
		if nodeMap[id] != nil {
			return
		}
		sig := fn.Type().(*types.Signature)
		recvType, isIface := receiverTypeName(sig)
		file, line := "", 0
		if fn.Pos().IsValid() {
			p := fset.Position(fn.Pos())
			file, line = p.Filename, p.Line
		}
		// Extract doc from AST
		doc := ""
		if pkg != nil && pkg.TypesInfo != nil {
			for _, f := range pkg.Syntax {
				ast.Inspect(f, func(n ast.Node) bool {
					fd, ok := n.(*ast.FuncDecl)
					if !ok {
						return true
					}
					obj := pkg.TypesInfo.Defs[fd.Name]
					if obj != nil && obj == fn && fd.Doc != nil {
						doc = strings.TrimSpace(fd.Doc.Text())
					}
					return true
				})
			}
		}
		nodeMap[id] = &data.FunctionInfo{
			ID:           id,
			Name:         fn.Name(),
			PkgPath:      pkgPath,
			PkgName:      fn.Pkg().Name(),
			Signature:    funcSig(fn),
			Doc:          doc,
			File:         file,
			Line:         line,
			CallerIDs:    []string{},
			CalleeIDs:    []string{},
			ReceiverType: recvType,
			IsInterface:  isIface,
		}
		if pkgMap[pkgPath] == nil {
			pkgMap[pkgPath] = &data.PackageInfo{ID: pkgPath, Name: fn.Pkg().Name()}
		}
	}

	// Pass 1: collect all functions in focused packages
	packages.Visit(pkgs, func(pkg *packages.Package) bool {
		if pkg.Types == nil || !shouldInclude(pkg.PkgPath, cfg) {
			return true
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			switch o := obj.(type) {
			case *types.Func:
				addFunc(o, pkg)
			case *types.TypeName:
				named, ok := o.Type().(*types.Named)
				if !ok {
					continue
				}
				for i := 0; i < named.NumMethods(); i++ {
					addFunc(named.Method(i), pkg)
				}
			}
		}
		return true
	}, nil)

	// Pass 2: inject interface method nodes BEFORE building edges so that
	// calls to interface methods (resolved via typesInfo.Selections) can be found.
	findImplementEdges(pkgs, nodeMap, fset, cfg)

	// Pass 3: walk AST call expressions to build edges
	callerMap := make(map[string][]string)
	calleeMap := make(map[string][]string)
	seenEdge := map[string]bool{}
	var edges []data.Edge

	addEdge := func(callerID, calleeID string) {
		if callerID == "" || calleeID == "" || callerID == calleeID {
			return
		}
		if nodeMap[callerID] == nil || nodeMap[calleeID] == nil {
			return
		}
		key := callerID + "→" + calleeID
		if seenEdge[key] {
			return
		}
		seenEdge[key] = true
		edges = append(edges, data.Edge{From: callerID, To: calleeID})
		callerMap[calleeID] = appendUnique(callerMap[calleeID], callerID)
		calleeMap[callerID] = appendUnique(calleeMap[callerID], calleeID)
	}

	packages.Visit(pkgs, func(pkg *packages.Package) bool {
		if pkg.TypesInfo == nil || !shouldInclude(pkg.PkgPath, cfg) {
			return true
		}
		info := pkg.TypesInfo

		for _, file := range pkg.Syntax {
			// Walk top-level function declarations
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				callerObj := info.Defs[fd.Name]
				if callerObj == nil {
					continue
				}
				callerFn, ok := callerObj.(*types.Func)
				if !ok {
					continue
				}
				callerID := staticFuncID(callerFn)
				if nodeMap[callerID] == nil {
					continue
				}

				// Walk the body, tracking closures on a stack
				walkCalls(fd.Body, info, callerID, func(calleeID string) {
					addEdge(callerID, calleeID)
				})
			}
		}
		return true
	}, nil)

	// Pass 4: collect implement edges (nodes already injected in Pass 2)
	implEdges := findImplementEdges(pkgs, nodeMap, fset, cfg)

	for _, fn := range nodeMap {
		if pkgMap[fn.PkgPath] == nil {
			pkgMap[fn.PkgPath] = &data.PackageInfo{ID: fn.PkgPath, Name: fn.PkgName}
		}
	}

	// Attach caller/callee lists
	for id, info := range nodeMap {
		if c := callerMap[id]; c != nil {
			info.CallerIDs = c
		}
		if c := calleeMap[id]; c != nil {
			info.CalleeIDs = c
		}
	}

	// Sort and assemble packages
	pkgPaths := make([]string, 0, len(pkgMap))
	for k := range pkgMap {
		pkgPaths = append(pkgPaths, k)
	}
	sort.Strings(pkgPaths)

	pkgList := make([]data.PackageInfo, 0, len(pkgMap))
	for _, path := range pkgPaths {
		pkg := *pkgMap[path]
		var fns []data.FunctionInfo
		for _, fn := range nodeMap {
			if fn.PkgPath == path {
				fns = append(fns, *fn)
			}
		}
		sort.Slice(fns, func(i, j int) bool {
			if fns[i].Line != fns[j].Line {
				return fns[i].Line < fns[j].Line
			}
			return fns[i].Name < fns[j].Name
		})
		pkg.Functions = fns
		pkgList = append(pkgList, pkg)
	}

	fmt.Printf("found %d packages, %d edges\n", len(pkgList), len(edges))

	return &data.GraphData{
		Packages:       pkgList,
		Edges:          edges,
		ImplementEdges: implEdges,
		FocusPkg:       cfg.Focus,
		ModulePath:     cfg.modulePath,
	}, nil
}

// walkCalls walks an AST node's call expressions and invokes emit for each resolved callee ID.
// Calls inside nested function literals are attributed to the same caller (conservative).
func walkCalls(node ast.Node, info *types.Info, callerID string, emit func(string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		calleeID := resolveCall(call, info)
		if calleeID != "" {
			emit(calleeID)
		}
		return true
	})
}

// resolveCall resolves a call expression to a canonical function ID using type info.
func resolveCall(call *ast.CallExpr, info *types.Info) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		// Method call: x.M() — look up in Selections first (covers interface & concrete dispatch)
		if sel, ok := info.Selections[fun]; ok {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return staticFuncID(fn)
			}
		}
		// Package-qualified call: pkg.Func()
		if obj, ok := info.Uses[fun.Sel]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return staticFuncID(fn)
			}
		}
	case *ast.Ident:
		if obj, ok := info.Uses[fun]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return staticFuncID(fn)
			}
		}
	}
	return ""
}

func receiverTypeName(sig *types.Signature) (name string, isIface bool) {
	recv := sig.Recv()
	if recv == nil {
		return "", false
	}
	t := recv.Type()
	if pt, ok := t.(*types.Pointer); ok {
		t = pt.Elem()
		if named, ok := t.(*types.Named); ok {
			_, iface := named.Underlying().(*types.Interface)
			return "*" + named.Obj().Name(), iface
		}
		return "", false
	}
	if named, ok := t.(*types.Named); ok {
		_, iface := named.Underlying().(*types.Interface)
		return named.Obj().Name(), iface
	}
	return "", false
}

func funcSig(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return fn.Name()
	}
	buf := &bytes.Buffer{}
	buf.WriteString("func ")
	if recv := sig.Recv(); recv != nil {
		buf.WriteString("(")
		if recv.Name() != "" {
			buf.WriteString(recv.Name())
			buf.WriteString(" ")
		}
		buf.WriteString(types.TypeString(recv.Type(), nil))
		buf.WriteString(") ")
	}
	buf.WriteString(fn.Name())
	buf.WriteString("(")
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if i > 0 {
			buf.WriteString(", ")
		}
		p := params.At(i)
		if p.Name() != "" {
			buf.WriteString(p.Name())
			buf.WriteString(" ")
		}
		if sig.Variadic() && i == params.Len()-1 {
			if sl, ok := p.Type().(*types.Slice); ok {
				buf.WriteString("...")
				buf.WriteString(types.TypeString(sl.Elem(), nil))
				continue
			}
		}
		buf.WriteString(types.TypeString(p.Type(), nil))
	}
	buf.WriteString(")")
	results := sig.Results()
	switch results.Len() {
	case 0:
	case 1:
		buf.WriteString(" ")
		r := results.At(0)
		if r.Name() != "" {
			buf.WriteString("(")
			buf.WriteString(r.Name())
			buf.WriteString(" ")
			buf.WriteString(types.TypeString(r.Type(), nil))
			buf.WriteString(")")
		} else {
			buf.WriteString(types.TypeString(r.Type(), nil))
		}
	default:
		buf.WriteString(" (")
		for i := 0; i < results.Len(); i++ {
			if i > 0 {
				buf.WriteString(", ")
			}
			r := results.At(i)
			if r.Name() != "" {
				buf.WriteString(r.Name())
				buf.WriteString(" ")
			}
			buf.WriteString(types.TypeString(r.Type(), nil))
		}
		buf.WriteString(")")
	}
	return buf.String()
}

func isStdLib(pkgPath, modulePath string) bool {
	if pkgPath == "" {
		return false
	}
	first := strings.SplitN(pkgPath, "/", 2)[0]
	if strings.Contains(first, ".") {
		return false
	}
	if modulePath != "" && (pkgPath == modulePath || strings.HasPrefix(pkgPath, modulePath+"/")) {
		return false
	}
	return true
}

func isVendor(pkgPath, modulePath string) bool {
	if strings.Contains(pkgPath, "/vendor/") {
		return true
	}
	if modulePath != "" && !strings.HasPrefix(pkgPath, modulePath) {
		return true
	}
	return false
}

func shouldInclude(pkgPath string, cfg Config) bool {
	if pkgPath == "" {
		return false
	}
	if cfg.ExcludeStd && isStdLib(pkgPath, cfg.modulePath) {
		return false
	}
	if cfg.ExcludeVendor && isVendor(pkgPath, cfg.modulePath) {
		return false
	}
	if cfg.Focus != "" && !strings.Contains(pkgPath, cfg.Focus) {
		return false
	}
	return true
}

func commonPathPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	prefix := paths[0]
	for _, p := range paths[1:] {
		for prefix != "" && !strings.HasPrefix(p, prefix+"/") && p != prefix {
			i := strings.LastIndex(prefix, "/")
			if i < 0 {
				return ""
			}
			prefix = prefix[:i]
		}
	}
	return prefix
}

func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// findImplementEdges injects interface method virtual nodes for all interfaces in focused
// packages, then finds pairs (interfaceMethod, concreteMethod) for implementations in scope.
func findImplementEdges(pkgs []*packages.Package, nodeMap map[string]*data.FunctionInfo, fset *token.FileSet, cfg Config) []data.ImplementEdge {
	type namedTypeEntry struct {
		named   *types.Named
		pkg     *packages.Package
		pkgPath string
	}
	var allTypes []namedTypeEntry

	packages.Visit(pkgs, func(pkg *packages.Package) bool {
		if pkg.Types == nil {
			return true
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			allTypes = append(allTypes, namedTypeEntry{named, pkg, pkg.PkgPath})
		}
		return true
	}, nil)

	var ifaces, concretes []namedTypeEntry
	for _, e := range allTypes {
		if _, ok := e.named.Underlying().(*types.Interface); ok {
			ifaces = append(ifaces, e)
		} else {
			concretes = append(concretes, e)
		}
	}

	injectIfaceNode := func(iface namedTypeEntry, method *types.Func) string {
		ifacePkgPath := iface.pkgPath
		ifaceTypeName := iface.named.Obj().Name()
		if !shouldInclude(ifacePkgPath, cfg) {
			return ""
		}
		id := fmt.Sprintf("(%s.%s).%s", ifacePkgPath, ifaceTypeName, method.Name())
		if nodeMap[id] != nil {
			return id
		}
		file, line := "", 0
		if pos := method.Pos(); pos.IsValid() && fset != nil {
			p := fset.Position(pos)
			file, line = p.Filename, p.Line
		}
		sig := method.Type().(*types.Signature)
		nodeMap[id] = &data.FunctionInfo{
			ID:           id,
			Name:         method.Name(),
			PkgPath:      ifacePkgPath,
			PkgName:      iface.pkg.Name,
			Signature:    types.TypeString(sig, nil),
			File:         file,
			Line:         line,
			CallerIDs:    []string{},
			CalleeIDs:    []string{},
			ReceiverType: ifaceTypeName,
			IsInterface:  true,
		}
		return id
	}

	for _, iface := range ifaces {
		ifaceType := iface.named.Underlying().(*types.Interface)
		for i := 0; i < ifaceType.NumMethods(); i++ {
			injectIfaceNode(iface, ifaceType.Method(i))
		}
	}

	var result []data.ImplementEdge
	seen := map[string]bool{}

	for _, iface := range ifaces {
		ifaceType := iface.named.Underlying().(*types.Interface)
		for _, conc := range concretes {
			if !shouldInclude(conc.pkgPath, cfg) {
				continue
			}
			for _, checkType := range []types.Type{conc.named, types.NewPointer(conc.named)} {
				if !types.Implements(checkType, ifaceType) {
					continue
				}
				isPtr := false
				if _, ok := checkType.(*types.Pointer); ok {
					isPtr = true
				}
				ms := types.NewMethodSet(checkType)
				for i := 0; i < ifaceType.NumMethods(); i++ {
					ifaceMethod := ifaceType.Method(i)
					ifaceID := injectIfaceNode(iface, ifaceMethod)
					if ifaceID == "" {
						continue
					}
					sel := ms.Lookup(conc.named.Obj().Pkg(), ifaceMethod.Name())
					if sel == nil {
						continue
					}
					concFunc, ok := sel.Obj().(*types.Func)
					if !ok {
						continue
					}
					concPkg := concFunc.Pkg()
					if concPkg == nil {
						continue
					}
					concRecvName := conc.named.Obj().Name()
					var concID string
					if isPtr {
						concID = fmt.Sprintf("(*%s.%s).%s", concPkg.Path(), concRecvName, ifaceMethod.Name())
					} else {
						concID = fmt.Sprintf("(%s.%s).%s", concPkg.Path(), concRecvName, ifaceMethod.Name())
					}
					if nodeMap[concID] == nil {
						continue
					}
					key := ifaceID + "→" + concID
					if seen[key] {
						continue
					}
					seen[key] = true
					result = append(result, data.ImplementEdge{
						InterfaceMethod: ifaceID,
						ConcreteMethod:  concID,
					})
				}
			}
		}
	}
	return result
}
