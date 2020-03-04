package car

import (
	"bytes"
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	bsrv "github.com/ipfs/go-blockservice"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/ipfs/go-filestore"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	format "github.com/ipfs/go-ipld-format"
	dag "github.com/ipfs/go-merkledag"
	dstest "github.com/ipfs/go-merkledag/test"
	ipldfree "github.com/ipld/go-ipld-prime/impl/free"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	"github.com/stretchr/testify/require"
)

func assertAddNodes(t *testing.T, ds format.DAGService, nds ...format.Node) {
	for _, nd := range nds {
		if err := ds.Add(context.Background(), nd); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoundtrip(t *testing.T) {
	dserv := dstest.Mock()
	a := dag.NewRawNode([]byte("aaaa"))
	b := dag.NewRawNode([]byte("bbbb"))
	c := dag.NewRawNode([]byte("cccc"))

	nd1 := &dag.ProtoNode{}
	nd1.AddNodeLink("cat", a)

	nd2 := &dag.ProtoNode{}
	nd2.AddNodeLink("first", nd1)
	nd2.AddNodeLink("dog", b)

	nd3 := &dag.ProtoNode{}
	nd3.AddNodeLink("second", nd2)
	nd3.AddNodeLink("bear", c)

	assertAddNodes(t, dserv, a, b, c, nd1, nd2, nd3)

	buf := new(bytes.Buffer)
	if err := WriteCar(context.Background(), dserv, []cid.Cid{nd3.Cid()}, buf); err != nil {
		t.Fatal(err)
	}

	bserv := dstest.Bserv()
	ch, err := LoadCar(bserv.Blockstore(), buf)
	if err != nil {
		t.Fatal(err)
	}

	if len(ch.Roots) != 1 {
		t.Fatal("should have one root")
	}

	if !ch.Roots[0].Equals(nd3.Cid()) {
		t.Fatal("got wrong cid")
	}

	bs := bserv.Blockstore()
	for _, nd := range []format.Node{a, b, c, nd1, nd2, nd3} {
		has, err := bs.Has(nd.Cid())
		if err != nil {
			t.Fatal(err)
		}

		if !has {
			t.Fatal("should have cid in blockstore")
		}
	}
}

func TestRoundtripFilestore(t *testing.T) {
	dserv := dstest.Mock()
	a := dag.NewRawNode([]byte("aaaa"))
	b := dag.NewRawNode([]byte("bbbb"))
	c := dag.NewRawNode([]byte("cccc"))

	nd1 := &dag.ProtoNode{}
	nd1.AddNodeLink("cat", a)

	nd2 := &dag.ProtoNode{}
	nd2.AddNodeLink("first", nd1)
	nd2.AddNodeLink("dog", b)

	nd3 := &dag.ProtoNode{}
	nd3.AddNodeLink("second", nd2)
	nd3.AddNodeLink("bear", c)

	assertAddNodes(t, dserv, a, b, c, nd1, nd2, nd3)

	os.Mkdir("_test", 0755)
	f, err := os.OpenFile("_test/sample.car", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	require.NoError(t, err)

	err = WriteCar(context.Background(), dserv, []cid.Cid{nd3.Cid()}, f)
	require.NoError(t, err)

	err = f.Close()
	require.NoError(t, err)

	ds := dssync.MutexWrap(datastore.NewMapDatastore())
	bs := dstest.Bserv().Blockstore()
	pwd, err := os.Getwd()
	require.NoError(t, err)
	fm := filestore.NewFileManager(ds, pwd)
	fm.AllowFiles = true
	fs := filestore.NewFilestore(bs, fm)

	f, err = os.Open("_test/sample.car")
	require.NoError(t, err)

	stat, err := f.Stat()
	require.NoError(t, err)

	path, err := filepath.Abs("_test/sample.car")
	require.NoError(t, err)

	rf, err := files.NewReaderPathFile(path, f, stat)
	require.NoError(t, err)

	_, err = LoadCar(fs, rf)
	require.NoError(t, err)

	err = f.Close()
	require.NoError(t, err)

	for _, nd := range []format.Node{a, b, c, nd1, nd2, nd3} {
		has, err := bs.Has(nd.Cid())
		require.NoError(t, err)
		require.False(t, has)

		has, err = fs.Has(nd.Cid())
		require.NoError(t, err)
		require.True(t, has)
	}
	newDserv := dag.NewDAGService(bsrv.New(fs, offline.Exchange(fs)))
	buf := new(bytes.Buffer)
	err = WriteCar(context.Background(), newDserv, []cid.Cid{nd3.Cid()}, buf)
	require.NoError(t, err)

	f, err = os.Open("_test/sample.car")
	require.NoError(t, err)

	fileBytes, err := ioutil.ReadAll(f)
	require.NoError(t, err)

	require.True(t, bytes.Equal(fileBytes, buf.Bytes()))
}
func TestRoundtripSelective(t *testing.T) {
	sourceBserv := dstest.Bserv()
	sourceBs := sourceBserv.Blockstore()
	dserv := dag.NewDAGService(sourceBserv)
	a := dag.NewRawNode([]byte("aaaa"))
	b := dag.NewRawNode([]byte("bbbb"))
	c := dag.NewRawNode([]byte("cccc"))

	nd1 := &dag.ProtoNode{}
	nd1.AddNodeLink("cat", a)

	nd2 := &dag.ProtoNode{}
	nd2.AddNodeLink("first", nd1)
	nd2.AddNodeLink("dog", b)
	nd2.AddNodeLink("repeat", nd1)

	nd3 := &dag.ProtoNode{}
	nd3.AddNodeLink("second", nd2)
	nd3.AddNodeLink("bear", c)

	assertAddNodes(t, dserv, a, b, c, nd1, nd2, nd3)

	ssb := builder.NewSelectorSpecBuilder(ipldfree.NodeBuilder())
	selector := ssb.ExploreFields(func(efsb builder.ExploreFieldsSpecBuilder) {
		efsb.Insert("Links",
			ssb.ExploreIndex(1, ssb.ExploreRecursive(selector.RecursionLimitNone(), ssb.ExploreAll(ssb.ExploreRecursiveEdge()))))
	}).Node()

	sc := NewSelectiveCar(context.Background(), sourceBs, []Dag{Dag{Root: nd3.Cid(), Selector: selector}})

	// write car in one step
	buf := new(bytes.Buffer)
	blockCount := 0
	err := sc.Write(buf, func(block Block) error {
		blockCount++
		return nil
	})
	require.Equal(t, blockCount, 5)
	require.NoError(t, err)

	// write car in two steps
	scp, err := sc.Prepare()
	require.NoError(t, err)
	buf2 := new(bytes.Buffer)
	err = scp.Dump(buf2)
	require.NoError(t, err)

	// verify preparation step correctly assesed length and blocks
	require.Equal(t, scp.Size(), uint64(buf.Len()))
	require.Equal(t, len(scp.Cids()), blockCount)

	// verify equal data written by both methods
	require.Equal(t, buf.Bytes(), buf2.Bytes())

	// readout car and verify contents
	bserv := dstest.Bserv()
	ch, err := LoadCar(bserv.Blockstore(), buf)
	require.NoError(t, err)
	require.Equal(t, len(ch.Roots), 1)

	require.True(t, ch.Roots[0].Equals(nd3.Cid()))

	bs := bserv.Blockstore()
	for _, nd := range []format.Node{a, b, nd1, nd2, nd3} {
		has, err := bs.Has(nd.Cid())
		require.NoError(t, err)
		require.True(t, has)
	}

	for _, nd := range []format.Node{c} {
		has, err := bs.Has(nd.Cid())
		require.NoError(t, err)
		require.False(t, has)
	}
}
