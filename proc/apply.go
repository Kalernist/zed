package proc

import (
	"fmt"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/expr"
	"github.com/brimdata/zed/zbuf"
)

//XXX this now seems sort of redundant with yield since we can apply any function
// to "this" with yield.  But this is used by cut etc...

type applier struct {
	pctx   *Context
	parent Interface
	expr   expr.Applier
	warned map[string]bool
}

func NewApplier(pctx *Context, parent Interface, apply expr.Applier) *applier {
	return &applier{
		pctx:   pctx,
		parent: parent,
		expr:   apply,
		warned: map[string]bool{},
	}
}

func (a *applier) Pull() (zbuf.Batch, error) {
	for {
		batch, err := a.parent.Pull()
		if EOS(batch, err) {
			if s := a.expr.Warning(); s != "" {
				a.maybeWarn(s)
			}
			return nil, err
		}
		scope := batch.Scope()
		vals := batch.Values()
		out := make([]zed.Value, 0, len(vals))
		for i := range vals {
			val := a.expr.Eval(&vals[i], scope)
			if val == zed.Missing {
				continue
			}
			// Copy is necessary because Apply can return
			// its argument.
			out = append(out, *val.Copy())
		}
		batch.Unref()
		if len(out) > 0 {
			//XXX bug - need to propagate scope
			return zbuf.NewArray(out), nil
		}
	}
}

func (a *applier) maybeWarn(s string) {
	if !a.warned[s] {
		a.pctx.Warnings <- fmt.Sprintf("%s: %s", a.expr, s)
		a.warned[s] = true
	}
}

func (a *applier) Done() {
	a.parent.Done()
}
