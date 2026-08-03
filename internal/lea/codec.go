package lea

import (
	"encoding/binary"
	"fmt"
	"time"
	"unsafe"

	"github.com/PizenLabs/izen/internal/lea/graph"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// Binary codec for graph.Snapshot. Used instead of gob because it decodes the
// full 20k-LOC snapshot in well under the 10ms startup budget.

type writer struct {
	buf []byte
}

func (w *writer) str(s string) {
	w.u32(uint32(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *writer) u32(v uint32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, v)
}

func (w *writer) u8(v byte) {
	w.buf = append(w.buf, v)
}

func (w *writer) i64(v int64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, uint64(v))
}

type reader struct {
	buf []byte
	pos int
}

func (r *reader) str() string {
	n := int(r.u32())
	if n < 0 || r.pos+n > len(r.buf) {
		return ""
	}
	s := unsafe.String(&r.buf[r.pos], n)
	r.pos += n
	return s
}

func (r *reader) u32() uint32 {
	if r.pos+4 > len(r.buf) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v
}

func (r *reader) u8() byte {
	if r.pos+1 > len(r.buf) {
		return 0
	}
	v := r.buf[r.pos]
	r.pos++
	return v
}

func (r *reader) i64() int64 {
	if r.pos+8 > len(r.buf) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return int64(v)
}

func (w *writer) node(n graph.Node) {
	w.str(n.ID)
	w.str(string(n.Kind))
	w.str(n.Name)
	w.str(n.QualName)
	w.str(n.Package)
	w.str(n.File)
	w.u32(uint32(n.Line))
	w.u32(uint32(n.EndLine))
	w.u8(boolByte(n.Exported))
	w.str(n.Signature)
	w.u32(uint32(len(n.Methods)))
	for _, m := range n.Methods {
		w.str(m)
	}
}

func readNode(r *reader) graph.Node {
	n := graph.Node{
		ID:        r.str(),
		Kind:      graph.NodeKind(r.str()),
		Name:      r.str(),
		QualName:  r.str(),
		Package:   r.str(),
		File:      r.str(),
		Line:      int(r.u32()),
		EndLine:   int(r.u32()),
		Exported:  r.u8() == 1,
		Signature: r.str(),
	}
	mc := int(r.u32())
	if mc > 0 {
		n.Methods = make([]string, 0, mc)
		for i := 0; i < mc; i++ {
			n.Methods = append(n.Methods, r.str())
		}
	}
	return n
}

func (w *writer) edge(e graph.Edge) {
	w.str(e.From)
	w.str(e.To)
	w.str(string(e.Kind))
	w.u32(uint32(e.Line))
}

func readEdge(r *reader) graph.Edge {
	return graph.Edge{
		From: r.str(),
		To:   r.str(),
		Kind: graph.EdgeKind(r.str()),
		Line: int(r.u32()),
	}
}

func (w *writer) extract(fe graph.FileExtract) {
	w.str(fe.File)
	w.u32(uint32(len(fe.Calls)))
	for _, c := range fe.Calls {
		w.str(c.Name)
		w.str(c.InFunc)
		w.u32(uint32(c.Line))
		w.u32(uint32(c.Column))
	}
	w.u32(uint32(len(fe.Routes)))
	for _, rt := range fe.Routes {
		w.str(rt.Path)
		w.str(rt.Method)
		w.str(rt.Handler)
		w.u32(uint32(rt.Line))
	}
}

func readExtract(r *reader) graph.FileExtract {
	fe := graph.FileExtract{File: r.str()}
	nc := int(r.u32())
	if nc > 0 {
		fe.Calls = make([]symbol.CallSite, 0, nc)
		for i := 0; i < nc; i++ {
			fe.Calls = append(fe.Calls, symbol.CallSite{
				Name:   r.str(),
				InFunc: r.str(),
				Line:   int(r.u32()),
				Column: int(r.u32()),
			})
		}
	}
	nr := int(r.u32())
	if nr > 0 {
		fe.Routes = make([]symbol.HTTPRoute, 0, nr)
		for i := 0; i < nr; i++ {
			fe.Routes = append(fe.Routes, symbol.HTTPRoute{
				Path:    r.str(),
				Method:  r.str(),
				Handler: r.str(),
				Line:    int(r.u32()),
			})
		}
	}
	return fe
}

// encodeSnapshot serializes a graph snapshot into binary form.
func encodeSnapshot(s graph.Snapshot) []byte {
	w := &writer{buf: make([]byte, 0, 1<<20)}
	w.str(s.Root)
	w.i64(s.BuiltAt.UnixNano())
	w.u32(uint32(len(s.Nodes)))
	for _, n := range s.Nodes {
		w.node(n)
	}
	w.u32(uint32(len(s.Edges)))
	for _, e := range s.Edges {
		w.edge(e)
	}
	w.u32(uint32(len(s.Extracts)))
	for _, fe := range s.Extracts {
		w.extract(fe)
	}
	return w.buf
}

// decodeSnapshot deserializes binary form back into a graph snapshot.
func decodeSnapshot(data []byte) (graph.Snapshot, error) {
	r := &reader{buf: data}
	s := graph.Snapshot{Root: r.str(), BuiltAt: time.Unix(0, r.i64())}
	nn := int(r.u32())
	if nn < 0 || r.pos < 0 || nn > len(data) {
		return s, fmt.Errorf("corrupt snapshot: bad node count")
	}
	if nn > 0 {
		s.Nodes = make([]graph.Node, 0, nn)
		for i := 0; i < nn; i++ {
			s.Nodes = append(s.Nodes, readNode(r))
		}
	}
	ne := int(r.u32())
	if ne > 0 {
		s.Edges = make([]graph.Edge, 0, ne)
		for i := 0; i < ne; i++ {
			s.Edges = append(s.Edges, readEdge(r))
		}
	}
	nf := int(r.u32())
	if nf > 0 {
		s.Extracts = make([]graph.FileExtract, 0, nf)
		for i := 0; i < nf; i++ {
			s.Extracts = append(s.Extracts, readExtract(r))
		}
	}
	return s, nil
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
