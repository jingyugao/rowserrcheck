package rowserr

import (
	"fmt"
	"go/ast"
	"go/types"
	"strconv"
	"strings"

	"github.com/gostaticanalysis/analysisutil"
	"github.com/jmoiron/sqlx"
	_ "github.com/jmoiron/sqlx"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

var _ = sqlx.NameMapper

func NewAnalyzer(sqlPkgs ...string) *analysis.Analyzer {
	dbsqlPkgs = append(dbsqlPkgs, sqlPkgs...)

	return &analysis.Analyzer{
		Name: "rowserr",
		Doc:  Doc,
		Run:  runx,
		Requires: []*analysis.Analyzer{
			buildssa.Analyzer,
		},
	}
}

const (
	Doc       = "rowserrcheck checks whether Rows.Err is checked"
	errMethod = "Err"
)

var (
	dbsqlPath string
	dbsqlPkgs = []string{
		"database/sql",
	}
)

type runner struct {
	pass      *analysis.Pass
	resObj    types.Object
	resTyp    *types.Pointer
	bodyObj   types.Object
	closeMthd *types.Func
	skipFile  map[*ast.File]bool
}

func runx(pass *analysis.Pass) (interface{}, error) {
	for _, pkg := range dbsqlPkgs {
		dbsqlPath = pkg
		ret, err := new(runner).runSimple(pass)
		if err != nil {
			return ret, err
		}
	}
	return nil, nil
}

// run executes an analysis for the pass. The receiver is passed
// by value because this func is called in parallel for different passes.
func (r runner) runSimple(pass *analysis.Pass) (interface{}, error) {
	r.pass = pass
	funcs := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs
	r.resObj = analysisutil.LookupFromImports(pass.Pkg.Imports(), dbsqlPath, "Rows")
	if r.resObj == nil {
		// skip checking
		return nil, nil
	}

	resNamed, ok := r.resObj.Type().(*types.Named)
	if !ok {
		return nil, fmt.Errorf("cannot find http.Response")
	}
	r.resTyp = types.NewPointer(resNamed)
	r.bodyObj = r.resObj
	if r.bodyObj == nil {
		return nil, fmt.Errorf("cannot find the object http.Response.Body")
	}
	bodyNamed := r.bodyObj.Type().(*types.Named)
	for i := 0; i < bodyNamed.NumMethods(); i++ {
		bmthd := bodyNamed.Method(i)
		if bmthd.Id() == errMethod {
			r.closeMthd = bmthd
		}
	}

	r.skipFile = map[*ast.File]bool{}
	for _, f := range funcs {
		if r.noImportedNetHTTP(f) {
			// skip this
			continue
		}

		// skip if the function is just referenced
		var isreffunc bool
		for i := 0; i < f.Signature.Results().Len(); i++ {
			if f.Signature.Results().At(i).Type().String() == r.resTyp.String() {
				isreffunc = true
			}
		}
		if isreffunc {
			continue
		}

		for _, b := range f.Blocks {
			for i := range b.Instrs {
				pos := b.Instrs[i].Pos()
				if r.isopen(b, i) {
					pass.Reportf(pos, fmt.Sprintf("rows err must be checked"))
				}
			}
		}
	}

	return nil, nil
}

func (r *runner) isopen(b *ssa.BasicBlock, i int) bool {
	call, ok := r.getReqCall(b.Instrs[i])
	if !ok {
		return false
	}

	if len(*call.Referrers()) == 0 {
		return true
	}
	cRefs := *call.Referrers()
	for _, cRef := range cRefs {
		val, ok := r.getResVal(cRef)
		if !ok {
			continue
		}

		if len(*val.Referrers()) == 0 {
			return true
		}

		resRefs := *val.Referrers()
		for _, resRef := range resRefs {
			switch resRef := resRef.(type) {
			case *ssa.Store: // Call in Closure function
				if len(*resRef.Addr.Referrers()) == 0 {
					return true
				}

				for _, aref := range *resRef.Addr.Referrers() {
					if c, ok := aref.(*ssa.MakeClosure); ok {
						f := c.Fn.(*ssa.Function)
						if r.noImportedNetHTTP(f) {
							// skip this
							return false
						}
						called := r.isClosureCalled(c)

						return r.calledInFunc(f, called)
					}

				}
			case *ssa.Call: // Indirect function call
				if r.isCloseCall(resRef) {
					return false
				}
				if f, ok := resRef.Call.Value.(*ssa.Function); ok {
					for _, b := range f.Blocks {
						for i := range b.Instrs {
							return r.isopen(b, i)
						}
					}
				}
			case *ssa.FieldAddr: // Normal reference to response entity
				if resRef.Referrers() == nil {
					return true
				}

				bRefs := *resRef.Referrers()

				for _, bRef := range bRefs {
					bOp, ok := r.getBodyOp(bRef)
					if !ok {
						continue
					}
					if len(*bOp.Referrers()) == 0 {
						return true
					}
					ccalls := *bOp.Referrers()
					for _, ccall := range ccalls {
						if r.isCloseCall(ccall) {
							return false
						}
					}
				}
			}
		}
	}

	return true
}

func (r *runner) getReqCall(instr ssa.Instruction) (*ssa.Call, bool) {
	call, ok := instr.(*ssa.Call)
	if !ok {
		return nil, false
	}
	if !strings.Contains(call.Type().String(), r.resTyp.String()) {
		return nil, false
	}
	return call, true
}

func (r *runner) getResVal(instr ssa.Instruction) (ssa.Value, bool) {
	switch instr := instr.(type) {
	case *ssa.FieldAddr:
		if instr.X.Type().String() == r.resTyp.String() {
			return instr.X.(ssa.Value), true
		}
	case *ssa.Call:
		if len(instr.Call.Args) == 1 && instr.Call.Args[0].Type().String() == r.resTyp.String() {
			return instr.Call.Args[0], true
		}
	case ssa.Value:
		if instr.Type().String() == r.resTyp.String() {
			return instr, true
		}
	}
	return nil, false
}

func (r *runner) getBodyOp(instr ssa.Instruction) (*ssa.UnOp, bool) {
	op, ok := instr.(*ssa.UnOp)
	if !ok {
		return nil, false
	}
	if op.Type() != r.bodyObj.Type() {
		return nil, false
	}
	return op, true
}

func (r *runner) isCloseCall(ccall ssa.Instruction) bool {
	switch ccall := ccall.(type) {
	case *ssa.Defer:
		if ccall.Call.Value != nil && ccall.Call.Value.Name() == r.closeMthd.Name() {
			return true
		}
	case *ssa.Call:
		if ccall.Call.Value != nil && ccall.Call.Value.Name() == r.closeMthd.Name() {
			return true
		}

	}
	return false
}

func (r *runner) isClosureCalled(c *ssa.MakeClosure) bool {
	refs := *c.Referrers()
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		switch ref.(type) {
		case *ssa.Call, *ssa.Defer:
			return true
		}
	}
	return false
}

func (r *runner) noImportedNetHTTP(f *ssa.Function) (ret bool) {
	obj := f.Object()
	if obj == nil {
		return false
	}

	file := analysisutil.File(r.pass, obj.Pos())
	if file == nil {
		return false
	}

	if skip, has := r.skipFile[file]; has {
		return skip
	}
	defer func() {
		r.skipFile[file] = ret
	}()

	for _, impt := range file.Imports {
		path, err := strconv.Unquote(impt.Path.Value)
		if err != nil {
			continue
		}
		path = analysisutil.RemoveVendor(path)
		if path == dbsqlPath {
			return false
		}
	}

	return true
}

func (r *runner) calledInFunc(f *ssa.Function, called bool) bool {
	for _, b := range f.Blocks {
		for i, instr := range b.Instrs {
			switch instr := instr.(type) {
			case *ssa.UnOp:
				refs := *instr.Referrers()
				if len(refs) == 0 {
					return true
				}
				for _, r := range refs {
					if v, ok := r.(ssa.Value); ok {
						vrefs := *v.Referrers()
						for _, vref := range vrefs {
							if vref, ok := vref.(*ssa.UnOp); ok {
								vrefs := *vref.Referrers()
								if len(vrefs) == 0 {
									return true
								}
								for _, vref := range vrefs {
									if c, ok := vref.(*ssa.Call); ok {
										if c.Call.Value != nil && c.Call.Value.Name() == errMethod {
											return !called
										}
									}
								}
							}
						}
					}

				}
			default:
				return r.isopen(b, i) || !called
			}
		}
	}
	return false
}

// isNamedType reports whether t is the named type path.name.
func isNamedType(t types.Type, path, name string) bool {
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj.Name() == name && obj.Pkg() != nil && obj.Pkg().Path() == path
}
