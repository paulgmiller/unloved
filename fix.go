package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"
)

type edit struct {
	start, end int
	text       string
}

func lowerName(name string) string {
	// HTTPClient -> httpClient, ID -> id, NewClient -> newClient.
	r := []rune(name)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	if n > 1 && n < len(r) {
		n--
	}
	for i := 0; i < n; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

func (c *checker) rename(findings []finding) error {
	if len(findings) == 0 {
		return nil
	}
	names := map[string]string{}
	for _, f := range findings {
		name := lowerName(f.name)
		if token.Lookup(name).IsKeyword() {
			return fmt.Errorf("%s: cannot rename %s to keyword %s", f.position, f.name, name)
		}
		if scope := f.object.Parent(); scope != nil {
			if other := scope.Lookup(name); other != nil {
				return fmt.Errorf("%s: %s conflicts with %s", f.position, name, other)
			}
		}
		names[c.key(f.object)] = name
	}
	var conflicts []error
	changes := map[string]map[int]edit{}
	add := func(pos token.Pos, end token.Pos, text string) {
		a, b := c.fset.Position(pos), c.fset.Position(end)
		if changes[a.Filename] == nil {
			changes[a.Filename] = map[int]edit{}
		}
		changes[a.Filename][a.Offset] = edit{a.Offset, b.Offset, text}
	}
	for _, p := range c.packages {
		for id, obj := range p.TypesInfo.Defs {
			if obj == nil {
				continue
			}
			name := names[c.key(obj)]
			if name == "" {
				continue
			}
			if fn, ok := obj.(*types.Func); ok {
				if recv := fn.Type().(*types.Signature).Recv(); recv != nil {
					if other, _, _ := types.LookupFieldOrMethod(recv.Type(), true, p.Types, name); other != nil {
						return fmt.Errorf("%s: method %s conflicts with %s", c.fset.Position(id.Pos()), name, other)
					}
				}
			}
			add(id.Pos(), id.End(), name)
		}
		selectors := map[*ast.Ident]bool{}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok {
					selectors[s.Sel] = true
				}
				return true
			})
		}
		for id, obj := range p.TypesInfo.Uses {
			name := names[c.key(obj)]
			if name == "" {
				continue
			}
			if obj.Parent() == obj.Pkg().Scope() && !selectors[id] {
				var inner *types.Scope
				for _, scope := range p.TypesInfo.Scopes {
					if scope.Contains(id.Pos()) && (inner == nil || scope.Pos() >= inner.Pos() && scope.End() <= inner.End()) {
						inner = scope
					}
				}
				if inner != nil {
					if _, other := inner.LookupParent(name, id.Pos()); other != nil {
						conflicts = append(conflicts, fmt.Errorf("%s: renaming %s to %s would be shadowed by %s", c.fset.Position(id.Pos()), id.Name, name, other))
					}
				}
			}
			add(id.Pos(), id.End(), name)
		}
		// Update the leading identifier of declaration documentation.
		for _, f := range p.Syntax {
			docs := func(g *ast.CommentGroup, id *ast.Ident) {
				if g == nil || id == nil {
					return
				}
				obj := p.TypesInfo.Defs[id]
				if obj == nil {
					return
				}
				name := names[c.key(obj)]
				if name == "" {
					return
				}
				for _, comment := range g.List {
					prefix := "// " + id.Name
					if strings.HasPrefix(comment.Text, prefix+" ") {
						add(comment.Pos()+3, comment.Pos()+3+token.Pos(len(id.Name)), name)
					}
				}
			}
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					docs(d.Doc, d.Name)
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							docs(d.Doc, s.Name)
							docs(s.Doc, s.Name)
						case *ast.ValueSpec:
							if len(s.Names) == 1 {
								docs(d.Doc, s.Names[0])
								docs(s.Doc, s.Names[0])
							}
						}
					}
				}
			}
		}
	}
	if len(conflicts) > 0 {
		return errors.Join(conflicts...)
	}
	// Validate every replacement before touching files, so conflicts and syntax
	// errors leave the working tree unchanged. Filesystem errors still surface.
	output := map[string][]byte{}
	for path, byOffset := range changes {
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		edits := make([]edit, 0, len(byOffset))
		for _, e := range byOffset {
			edits = append(edits, e)
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
		for _, e := range edits {
			src = append(append(append([]byte{}, src[:e.start]...), []byte(e.text)...), src[e.end:]...)
		}
		src, err = format.Source(src)
		if err != nil {
			return fmt.Errorf("format %s: %w", path, err)
		}
		output[path] = src
	}
	// Type-check the complete proposed edit before writing, including imports,
	// interface assignments, and test variants that renaming can affect.
	cfg := *c.config
	cfg.Fset = token.NewFileSet()
	cfg.Overlay = output
	proposed, err := packages.Load(&cfg, "./...")
	if err != nil {
		return fmt.Errorf("validate rename: %w", err)
	}
	var typeErrors []string
	packages.Visit(proposed, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			typeErrors = append(typeErrors, e.Error())
		}
	})
	if len(typeErrors) > 0 {
		return fmt.Errorf("rename would break type checking:\n%s", strings.Join(typeErrors, "\n"))
	}
	for path, src := range output {
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
