package car

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	files "github.com/ipfs/go-ipfs-files"
	posinfo "github.com/ipfs/go-ipfs-posinfo"
	cbor "github.com/ipfs/go-ipld-cbor"
	format "github.com/ipfs/go-ipld-format"
	dag "github.com/ipfs/go-merkledag"

	util "github.com/ipfs/go-car/util"
)

func init() {
	cbor.RegisterCborType(CarHeader{})
}

type Store interface {
	Put(blocks.Block) error
}

type ReadStore interface {
	Get(cid.Cid) (blocks.Block, error)
}

type CarHeader struct {
	Roots   []cid.Cid
	Version uint64
}

type carWriter struct {
	ds   format.DAGService
	w    io.Writer
	walk WalkFunc
}

type WalkFunc func(format.Node) ([]*format.Link, error)

func WriteCar(ctx context.Context, ds format.DAGService, roots []cid.Cid, w io.Writer) error {
	return WriteCarWithWalker(ctx, ds, roots, w, DefaultWalkFunc)
}

func WriteCarWithWalker(ctx context.Context, ds format.DAGService, roots []cid.Cid, w io.Writer, walk WalkFunc) error {

	h := &CarHeader{
		Roots:   roots,
		Version: 1,
	}

	if err := WriteHeader(h, w); err != nil {
		return fmt.Errorf("failed to write car header: %s", err)
	}

	cw := &carWriter{ds: ds, w: w, walk: walk}
	seen := cid.NewSet()
	for _, r := range roots {
		if err := dag.Walk(ctx, cw.enumGetLinks, r, seen.Visit); err != nil {
			return err
		}
	}
	return nil
}

func DefaultWalkFunc(nd format.Node) ([]*format.Link, error) {
	return nd.Links(), nil
}

func ReadHeader(br *bufio.Reader) (*CarHeader, uint64, error) {
	hb, l, err := util.LdRead(br)
	if err != nil {
		return nil, 0, err
	}

	var ch CarHeader
	if err := cbor.DecodeInto(hb, &ch); err != nil {
		return nil, 0, err
	}

	return &ch, l, nil
}

func WriteHeader(h *CarHeader, w io.Writer) error {
	hb, err := cbor.DumpObject(h)
	if err != nil {
		return err
	}

	return util.LdWrite(w, hb)
}

func SizeHeader(h *CarHeader) (uint64, error) {
	hb, err := cbor.DumpObject(h)
	if err != nil {
		return 0, err
	}

	return util.LdSize(hb), nil
}

func (cw *carWriter) enumGetLinks(ctx context.Context, c cid.Cid) ([]*format.Link, error) {
	nd, err := cw.ds.Get(ctx, c)
	if err != nil {
		return nil, err
	}

	if err := cw.writeNode(ctx, nd); err != nil {
		return nil, err
	}

	return cw.walk(nd)
}

func (cw *carWriter) writeNode(ctx context.Context, nd format.Node) error {
	return util.LdWrite(cw.w, nd.Cid().Bytes(), nd.RawData())
}

type carReader struct {
	br       *bufio.Reader
	offset   uint64
	fullPath string
	stat     os.FileInfo
	Header   *CarHeader
}

func NewCarReader(r io.Reader) (*carReader, error) {

	br := bufio.NewReader(r)
	ch, offset, err := ReadHeader(br)
	if err != nil {
		return nil, err
	}

	if len(ch.Roots) == 0 {
		return nil, fmt.Errorf("empty car")
	}

	if ch.Version != 1 {
		return nil, fmt.Errorf("invalid car version: %d", ch.Version)
	}

	cr := &carReader{
		br:     br,
		offset: offset,
		Header: ch,
	}
	if fi, ok := r.(files.FileInfo); ok {
		cr.fullPath = fi.AbsPath()
		cr.stat = fi.Stat()
	}
	return cr, nil
}

func (cr *carReader) Next() (blocks.Block, error) {
	c, l, data, err := util.ReadNode(cr.br)
	if err != nil {
		return nil, err
	}

	hashed, err := c.Prefix().Sum(data)
	if err != nil {
		return nil, err
	}

	if !hashed.Equals(c) {
		return nil, fmt.Errorf("mismatch in content integrity, name: %s, data: %s", c, hashed)
	}

	offset := cr.offset + l - uint64(len(data))
	cr.offset += l

	blk, err := blocks.NewBlockWithCid(data, c)
	if cr.fullPath == "" {
		return blk, err
	}
	nd, err := format.Decode(blk)
	if err != nil {
		return nil, err
	}
	return &posinfo.FilestoreNode{
		Node: nd,
		PosInfo: &posinfo.PosInfo{
			Offset:   offset,
			FullPath: cr.fullPath,
			Stat:     cr.stat,
		},
	}, nil
}

type batchStore interface {
	PutMany([]blocks.Block) error
}

func LoadCar(s Store, r io.Reader) (*CarHeader, error) {
	cr, err := NewCarReader(r)
	if err != nil {
		return nil, err
	}

	if bs, ok := s.(batchStore); ok {
		return loadCarFast(bs, cr)
	}

	return loadCarSlow(s, cr)
}

func loadCarFast(s batchStore, cr *carReader) (*CarHeader, error) {
	var buf []blocks.Block
	for {
		blk, err := cr.Next()
		switch err {
		case io.EOF:
			if len(buf) > 0 {
				if err := s.PutMany(buf); err != nil {
					return nil, err
				}
			}
			return cr.Header, nil
		default:
			return nil, err
		case nil:
		}

		buf = append(buf, blk)

		if len(buf) > 1000 {
			if err := s.PutMany(buf); err != nil {
				return nil, err
			}
			buf = buf[:0]
		}
	}

	if len(buf) > 0 {
		if err := s.PutMany(buf); err != nil {
			return nil, err
		}
	}

	return cr.Header, nil
}

func loadCarSlow(s Store, cr *carReader) (*CarHeader, error) {

	for {
		blk, err := cr.Next()
		switch err {
		case io.EOF:
			return cr.Header, nil
		default:
			return nil, err
		case nil:
		}

		if err := s.Put(blk); err != nil {
			return nil, err
		}
	}
}
