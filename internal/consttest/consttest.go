// Package consttest reads constant declarations from Go source, for tests that check an enumerator against the
// constants it lists.
package consttest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// Const is one declared constant. Value is the unquoted string literal; empty for any other value, such as iota.
type Const struct {
	Name  string
	Value string
}

// Declared returns the constants of typeName in the Go file at path, in declaration order. A constant without a
// type or value in a const block has the type of the one above it.
func Declared(path, typeName string) ([]Const, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var out []Const

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}

		var current string

		for _, spec := range gen.Specs {
			vs, _ := spec.(*ast.ValueSpec)

			switch typ, isIdent := vs.Type.(*ast.Ident); {
			case isIdent:
				current = typ.Name
			case vs.Type != nil || len(vs.Values) > 0:
				current = ""
			}

			if current != typeName {
				continue
			}

			for i, name := range vs.Names {
				c := Const{Name: name.Name}

				if i < len(vs.Values) {
					if lit, isLit := vs.Values[i].(*ast.BasicLit); isLit && lit.Kind == token.STRING {
						c.Value, _ = strconv.Unquote(lit.Value)
					}
				}

				out = append(out, c)
			}
		}
	}

	return out, nil
}
