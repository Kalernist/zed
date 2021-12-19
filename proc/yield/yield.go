package yield

import (
	"github.com/brimdata/zed"
	"github.com/brimdata/zed/expr"
	"github.com/brimdata/zed/proc"
	"github.com/brimdata/zed/zbuf"
)

type Proc struct {
	parent proc.Interface
	exprs  []expr.Evaluator
}

func New(parent proc.Interface, exprs []expr.Evaluator) *Proc {
	return &Proc{
		parent: parent,
		exprs:  exprs,
	}
}

func (p *Proc) Pull() (zbuf.Batch, error) {
	for {
		batch, err := p.parent.Pull()
		if proc.EOS(batch, err) {
			return nil, err
		}
		scope := batch.Scope()
		vals := batch.Values()
		recs := make([]zed.Value, 0, len(p.exprs)*len(vals))
		for i := range vals {
			for _, e := range p.exprs {
				out := e.Eval(&vals[i], scope)
				if out == zed.Missing {
					continue
				}
				// Copy is necessary because argument bytes
				// can be reused.
				recs = append(recs, *out.Copy())
			}
		}
		batch.Unref()
		if len(recs) > 0 {
			return zbuf.NewArray(recs), nil
		}
	}
}

func (p *Proc) Done() {
	p.parent.Done()
}
