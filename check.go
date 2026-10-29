package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type finding struct {
	position token.Position
	name     string
	object   types.Object
}

type checker struct {
	config     *packages.Config
	fset       *token.FileSet
	packages   []*packages.Package
	candidates map[string]types.Object
	required   map[string]bool
}

// Position keys unify objects from ordinary packages and their test variants,
// including instantiated generic methods. Pointer identity doesn't do that.
func (c *checker) key(o types.Object) string {
	if f, ok := o.(*types.Func); ok {
		o = f.Origin()
	}
	p := c.fset.Position(o.Pos())
	return fmt.Sprintf("%s:%d", p.Filename, p.Offset)
}

func run(dir, tags string, fix bool) ([]finding, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	c := &checker{fset: token.NewFileSet(), candidates: map[string]types.Object{}, required: map[string]bool{}}
	cfg := &packages.Config{
		Dir: dir, Fset: c.fset, Tests: true,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	c.config = cfg
	c.packages, err = packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load module: %w", err)
	}
	if len(c.packages) == 0 {
		return nil, fmt.Errorf("no packages found in %s", dir)
	}
	var loadErrors []string
	packages.Visit(c.packages, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrors = append(loadErrors, e.Error())
		}
	})
	if len(loadErrors) != 0 {
		return nil, fmt.Errorf("load module:\n%s", strings.Join(loadErrors, "\n"))
	}
	generated := map[string]bool{}
	for _, p := range c.packages {
		for _, f := range p.Syntax {
			if ast.IsGenerated(f) {
				generated[c.fset.Position(f.Pos()).Filename] = true
			}
			if ast.IsGenerated(f) || strings.HasSuffix(c.fset.Position(f.Pos()).Filename, "_test.go") {
				continue
			}
			if err := c.declarations(p, f); err != nil {
				return nil, err
			}
		}
		// External tests count as consumers, just like other packages. Same-package
		// tests don't justify an export but their references are renamed by -fix.
		for id, obj := range p.TypesInfo.Uses {
			if obj.Pkg() != nil && (obj.Pkg().Path() != p.PkgPath || generated[c.fset.Position(id.Pos()).Filename]) {
				c.required[c.key(obj)] = true
			}
		}
	}
	c.interfaces()
	// An embedded field's name follows the embedded type. Preserve embedded
	// types so this cleanup never changes a struct field's name.
	for _, p := range c.packages {
		for _, tv := range p.TypesInfo.Types {
			if st, ok := tv.Type.Underlying().(*types.Struct); ok {
				for i := 0; i < st.NumFields(); i++ {
					field := st.Field(i)
					if !field.Embedded() {
						continue
					}
					t := field.Type()
					if ptr, ok := t.(*types.Pointer); ok {
						t = ptr.Elem()
					}
					switch t := t.(type) {
					case *types.Named:
						c.required[c.key(t.Obj())] = true
					case *types.Alias:
						c.required[c.key(t.Obj())] = true
					}
				}
			}
		}
	}
	var findings []finding
	for k, obj := range c.candidates {
		if !c.required[k] {
			findings = append(findings, finding{c.fset.Position(obj.Pos()), obj.Name(), obj})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i].position, findings[j].position
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Offset < b.Offset
	})
	if fix {
		if err := c.rename(findings); err != nil {
			return nil, err
		}
	}
	return findings, nil
}

// Explicit exceptions need a reason and apply to one declaration.
func directive(groups ...*ast.CommentGroup) (bool, error) {
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, comment := range g.List {
			text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			if !strings.HasPrefix(text, "unexport:ignore") {
				continue
			}
			if strings.TrimSpace(strings.TrimPrefix(text, "unexport:ignore")) == "" {
				return false, fmt.Errorf("unexport:ignore requires a reason")
			}
			return true, nil
		}
	}
	return false, nil
}

func (c *checker) add(p *packages.Package, id *ast.Ident, keep bool) {
	obj := p.TypesInfo.Defs[id]
	if obj == nil || !obj.Exported() {
		return
	}
	if keep {
		c.required[c.key(obj)] = true
		return
	}
	c.candidates[c.key(obj)] = obj
}

func (c *checker) declarations(p *packages.Package, f *ast.File) error {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			keep, err := directive(d.Doc)
			if err != nil {
				return fmt.Errorf("%s: %w", c.fset.Position(d.Pos()), err)
			}
			c.add(p, d.Name, keep)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					keep, err := directive(d.Doc, s.Doc, s.Comment)
					if err != nil {
						return fmt.Errorf("%s: %w", c.fset.Position(s.Pos()), err)
					}
					c.add(p, s.Name, keep)
				case *ast.ValueSpec:
					keep, err := directive(d.Doc, s.Doc, s.Comment)
					if err != nil {
						return fmt.Errorf("%s: %w", c.fset.Position(s.Pos()), err)
					}
					for _, name := range s.Names {
						c.add(p, name, keep)
					}
				}
			}
		}
	}
	return nil
}

// Keep methods that implement any interface visible in the module or its
// dependencies, even if no direct selector references the concrete method.
func (c *checker) interfaces() {
	ifaces := map[*types.Interface]bool{}
	seen := map[types.Type]bool{}
	var visit func(types.Type)
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Alias:
			visit(types.Unalias(t))
		case *types.Named:
			visit(t.Underlying())
			for i := 0; i < t.NumMethods(); i++ {
				visit(t.Method(i).Type())
			}
		case *types.Interface:
			t.Complete()
			if t.NumMethods() > 0 && t.IsMethodSet() {
				ifaces[t] = true
			}
			for i := 0; i < t.NumMethods(); i++ {
				visit(t.Method(i).Type())
			}
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Struct:
			for i := 0; i < t.NumFields(); i++ {
				visit(t.Field(i).Type())
			}
		case *types.Signature:
			visit(t.Params())
			visit(t.Results())
		case *types.Tuple:
			for i := 0; i < t.Len(); i++ {
				visit(t.At(i).Type())
			}
		}
	}
	packages.Visit(c.packages, nil, func(p *packages.Package) {
		if p.Types != nil {
			for _, name := range p.Types.Scope().Names() {
				visit(p.Types.Scope().Lookup(name).Type())
			}
		}
		if p.TypesInfo != nil {
			for _, tv := range p.TypesInfo.Types {
				visit(tv.Type)
			}
		}
	})
	// Check complete method sets: a method on an embedded type may satisfy an
	// interface only when promoted onto a larger outer type.
	for t := range seen {
		switch t.(type) {
		case *types.Named, *types.Struct:
		default:
			continue
		}
		for _, recv := range []types.Type{t, types.NewPointer(t)} {
			methods := types.NewMethodSet(recv)
			relevant := false
			for i := 0; i < methods.Len(); i++ {
				if c.candidates[c.key(methods.At(i).Obj())] != nil {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
			for iface := range ifaces {
				if !types.Implements(recv, iface) {
					continue
				}
				for i := 0; i < iface.NumMethods(); i++ {
					method := iface.Method(i)
					if sel := methods.Lookup(method.Pkg(), method.Name()); sel != nil {
						c.required[c.key(sel.Obj())] = true
					}
				}
			}
		}
	}
}
