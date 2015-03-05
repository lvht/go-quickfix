// Package quickfix provides functions to rewrite Go AST
// that is typed well but "go build" fails to pass building.
package quickfix

import (
	"fmt"
	"regexp"
	"strings"

	"go/ast"
	"go/token"
	"golang.org/x/tools/go/ast/astutil"
	_ "golang.org/x/tools/go/gcimporter"
	"golang.org/x/tools/go/types"
)

var (
	declaredNotUsed       = regexp.MustCompile(`^([a-zA-Z0-9_]+) declared but not used$`)
	importedNotUsed       = regexp.MustCompile(`^(".+") imported but not used$`)
	noNewVariableOnDefine = "no new variables on left side of :="
)

// QuickFix rewrites AST files of same package so that they pass go build.
// For example:
//   v declared but not used             -> append `_ = v`
//   "p" imported but not used           -> rewrite to `import _ "p"`
//   no new variables on left side of := -> rewrite `:=` to `=`
//
// TODO hardMode, which removes errorneous code rather than adding
func QuickFix(fset *token.FileSet, files []*ast.File) (err error) {
	for i := 0; i < 10; i++ {
		err = quickFix1(fset, files)
		if err == nil {
			return
		}
	}

	return
}

func quickFix1(fset *token.FileSet, files []*ast.File) error {
	errs := []error{}
	config := &types.Config{
		Error: func(err error) {
			errs = append(errs, err)
		},
	}

	_, err := config.Check("_quickfix", fset, files, nil)
	if err == nil {
		return nil
	}

	// apply fixes on AST later so that we won't break funcs that inspect AST by positions
	fixes := []func() error{}
	unhandled := errorList{}

	for _, err := range errs {
		err, ok := err.(types.Error)
		if !ok || !err.Soft {
			unhandled = append(unhandled, err)
			continue
		}

		f := findFile(files, err.Pos)
		if f == nil {
			err := fmt.Errorf("cannot find file for error %q: %s (%d)", err.Error(), fset.Position(err.Pos), err.Pos)
			unhandled = append(unhandled, err)
			continue
		}

		nodepath, _ := astutil.PathEnclosingInterval(f, err.Pos, err.Pos)

		var fix func() error

		// - "%s declared but not used"
		// - "%q imported but not used"
		// - "label %s declared but not used" TODO
		// - "no new variables on left side of :="
		if m := declaredNotUsed.FindStringSubmatch(err.Msg); m != nil {
			ident := m[1]

			// insert "_ = x" to supress "declared but not used" error
			stmt := &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent("_")},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{ast.NewIdent(ident)},
			}

			fix = func() error {
				if appendStmt(nodepath, stmt) {
					return nil
				}
				return err
			}
		} else if m := importedNotUsed.FindStringSubmatch(err.Msg); m != nil {
			pkgPath := m[1] // quoted string, but it's okay because this will be compared to ast.BasicLit.Value.

			fix = func() error {
				for _, imp := range f.Imports {
					if imp.Path.Value == pkgPath {
						// make this import spec anonymous one
						imp.Name = ast.NewIdent("_")
						return nil
					}
				}

				return err
			}
		} else if err.Msg == noNewVariableOnDefine {
			fix = func() error {
				for _, node := range nodepath {
					if assign, ok := node.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
						assign.Tok = token.ASSIGN
						return nil
					}
				}
				return err
			}
		} else {
			unhandled = append(unhandled, err)
		}

		if fix != nil {
			fixes = append(fixes, fix)
		}
	}

	for _, fix := range fixes {
		err := fix()
		if err != nil {
			unhandled = append(unhandled, err)
		}
	}

	return unhandled.any()
}

type errorList []error

func (b errorList) any() error {
	if len(b) == 0 {
		return nil
	}

	return b
}

func (b errorList) Error() string {
	s := []string{fmt.Sprintf("%d error(s):", len(b))}
	for _, e := range b {
		s = append(s, fmt.Sprintf("- %s", e))
	}
	return strings.Join(s, "\n")
}

func appendStmt(nodepath []ast.Node, stmt ast.Stmt) bool {
	for _, node := range nodepath {
		switch node := node.(type) {
		case *ast.BlockStmt:
			if node.List == nil {
				node.List = []ast.Stmt{}
			}
			node.List = append(node.List, stmt)

		case *ast.CaseClause:
			if node.Body == nil {
				node.Body = []ast.Stmt{}
			}
			node.Body = append(node.Body, stmt)

		case *ast.CommClause:
			if node.Body == nil {
				node.Body = []ast.Stmt{}
			}
			node.Body = append(node.Body, stmt)

		default:
			continue
		}

		return true
	}

	return false
}

func findFile(files []*ast.File, pos token.Pos) *ast.File {
	for _, f := range files {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}

	return nil
}
