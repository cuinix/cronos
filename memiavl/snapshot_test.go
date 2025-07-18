package memiavl

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotEncodingRoundTrip(t *testing.T) {
	// setup test tree
	tree := New(0)
	for _, changes := range ChangeSets[:len(ChangeSets)-1] {
		tree.ApplyChangeSet(changes)
		_, _, err := tree.SaveVersion(true)
		require.NoError(t, err)
	}

	snapshotDir := t.TempDir()
	require.NoError(t, tree.WriteSnapshot(snapshotDir))

	snapshot, err := OpenSnapshot(snapshotDir)
	require.NoError(t, err)

	tree2 := NewFromSnapshot(snapshot, true, 0)

	require.Equal(t, tree.Version(), tree2.Version())
	require.Equal(t, tree.RootHash(), tree2.RootHash())

	// verify all the node hashes in snapshot
	for i := 0; i < snapshot.nodesLen(); i++ {
		node := snapshot.Node(uint32(i))
		require.Equal(t, node.Hash(), HashNode(node))
	}

	require.NoError(t, snapshot.Close())

	// test modify tree loaded from snapshot
	snapshot, err = OpenSnapshot(snapshotDir)
	require.NoError(t, err)
	tree3 := NewFromSnapshot(snapshot, true, 0)
	tree3.ApplyChangeSet(ChangeSets[len(ChangeSets)-1])
	hash, v, err := tree3.SaveVersion(true)
	require.NoError(t, err)
	require.Equal(t, RefHashes[len(ChangeSets)-1], hash)
	require.Equal(t, len(ChangeSets), int(v))
	require.NoError(t, snapshot.Close())
}

func TestSnapshotExport(t *testing.T) {
	expNodes := []*ExportNode{
		{Key: []byte("hello"), Value: []byte("world1"), Version: 2, Height: 0},
		{Key: []byte("hello1"), Value: []byte("world1"), Version: 2, Height: 0},
		{Key: []byte("hello1"), Value: nil, Version: 3, Height: 1},
		{Key: []byte("hello2"), Value: []byte("world1"), Version: 3, Height: 0},
		{Key: []byte("hello3"), Value: []byte("world1"), Version: 3, Height: 0},
		{Key: []byte("hello3"), Value: nil, Version: 3, Height: 1},
		{Key: []byte("hello2"), Value: nil, Version: 3, Height: 2},
	}

	// setup test tree
	tree := New(0)
	for _, changes := range ChangeSets[:3] {
		tree.ApplyChangeSet(changes)
		_, _, err := tree.SaveVersion(true)
		require.NoError(t, err)
	}

	snapshotDir := t.TempDir()
	require.NoError(t, tree.WriteSnapshot(snapshotDir))

	snapshot, err := OpenSnapshot(snapshotDir)
	require.NoError(t, err)

	var nodes []*ExportNode
	exporter := snapshot.Export()
	for {
		node, err := exporter.Next()
		if errors.Is(err, ErrorExportDone) {
			break
		}
		require.NoError(t, err)
		nodes = append(nodes, node)
	}

	require.Equal(t, expNodes, nodes)
}

func TestSnapshotImportExport(t *testing.T) {
	// setup test tree
	tree := New(0)
	for _, changes := range ChangeSets {
		tree.ApplyChangeSet(changes)
		_, _, err := tree.SaveVersion(true)
		require.NoError(t, err)
	}

	snapshotDir := t.TempDir()
	require.NoError(t, tree.WriteSnapshot(snapshotDir))
	snapshot, err := OpenSnapshot(snapshotDir)
	require.NoError(t, err)

	ch := make(chan *ExportNode)

	go func() {
		defer close(ch)

		exporter := snapshot.Export()
		for {
			node, err := exporter.Next()
			if errors.Is(err, ErrorExportDone) {
				break
			}
			require.NoError(t, err)
			ch <- node
		}
	}()

	snapshotDir2 := t.TempDir()
	err = doImport(snapshotDir2, tree.Version(), ch)
	require.NoError(t, err)

	snapshot2, err := OpenSnapshot(snapshotDir2)
	require.NoError(t, err)
	require.Equal(t, snapshot.RootNode().Hash(), snapshot2.RootNode().Hash())

	// verify all the node hashes in snapshot
	for i := 0; i < snapshot2.nodesLen(); i++ {
		node := snapshot2.Node(uint32(i))
		require.Equal(t, node.Hash(), HashNode(node))
	}
}

func TestDBSnapshotRestore(t *testing.T) {
	db, err := Load(t.TempDir(), Options{
		CreateIfMissing:   true,
		InitialStores:     []string{"test", "test2"},
		AsyncCommitBuffer: -1,
	})
	require.NoError(t, err)

	for _, changes := range ChangeSets {
		cs := []*NamedChangeSet{
			{
				Name:      "test",
				Changeset: changes,
			},
			{
				Name:      "test2",
				Changeset: changes,
			},
		}
		require.NoError(t, db.ApplyChangeSets(cs))
		_, err := db.Commit()
		require.NoError(t, err)

		testSnapshotRoundTrip(t, db)
	}

	require.NoError(t, db.RewriteSnapshot())
	require.NoError(t, db.Reload())
	require.Equal(t, len(ChangeSets), int(db.metadata.CommitInfo.Version))
	testSnapshotRoundTrip(t, db)
}

func testSnapshotRoundTrip(t *testing.T, db *DB) {
	t.Helper()
	exporter, err := NewMultiTreeExporter(db.dir, uint32(db.Version()), true)
	require.NoError(t, err)

	restoreDir := t.TempDir()
	importer, err := NewMultiTreeImporter(restoreDir, uint64(db.Version()))
	require.NoError(t, err)

	for {
		item, err := exporter.Next()
		if errors.Is(err, ErrorExportDone) {
			break
		}
		require.NoError(t, err)
		require.NoError(t, importer.Add(item))
	}

	require.NoError(t, importer.Finalize())
	require.NoError(t, importer.Close())
	require.NoError(t, exporter.Close())

	db2, err := Load(restoreDir, Options{})
	require.NoError(t, err)
	require.Equal(t, db.LastCommitInfo(), db2.LastCommitInfo())

	// the imported db function normally
	_, err = db2.Commit()
	require.NoError(t, err)
}
