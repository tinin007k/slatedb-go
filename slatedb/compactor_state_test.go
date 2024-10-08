package slatedb

import (
	"github.com/slatedb/slatedb-go/slatedb/common"
	"github.com/stretchr/testify/assert"
	"github.com/thanos-io/objstore"
	"testing"
	"time"
)

var testPath = "/test/db"

func TestShouldRegisterCompactionAsSubmitted(t *testing.T) {
	_, _, state := buildTestState()
	err := state.submitCompaction(buildL0Compaction(state.dbState.l0, 0))
	assert.NoError(t, err)

	assert.Equal(t, 1, len(state.compactions))
	assert.Equal(t, Submitted, state.compactions[0].status)
}

func TestShouldUpdateDBStateWhenCompactionFinished(t *testing.T) {
	_, _, state := buildTestState()
	beforeCompaction := state.dbState.clone()
	compaction := buildL0Compaction(beforeCompaction.l0, 0)
	err := state.submitCompaction(compaction)
	assert.NoError(t, err)

	sr := SortedRun{
		id:      0,
		sstList: beforeCompaction.l0,
	}
	state.finishCompaction(sr.clone())

	compactedID, _ := beforeCompaction.l0[0].id.compactedID().Get()
	l0LastCompacted, _ := state.dbState.l0LastCompacted.Get()
	assert.Equal(t, compactedID, l0LastCompacted)
	assert.Equal(t, 0, len(state.dbState.l0))
	assert.Equal(t, 1, len(state.dbState.compacted))
	assert.Equal(t, sr.id, state.dbState.compacted[0].id)
	compactedSR := state.dbState.compacted[0]
	for i := 0; i < len(sr.sstList); i++ {
		assert.Equal(t, sr.sstList[i].id, compactedSR.sstList[i].id)
	}
}

func TestShouldRemoveCompactionWhenCompactionFinished(t *testing.T) {
	_, _, state := buildTestState()
	beforeCompaction := state.dbState.clone()
	compaction := buildL0Compaction(beforeCompaction.l0, 0)
	err := state.submitCompaction(compaction)
	assert.NoError(t, err)

	sr := SortedRun{
		id:      0,
		sstList: beforeCompaction.l0,
	}
	state.finishCompaction(sr.clone())

	assert.Equal(t, 0, len(state.compactions))
}

func TestShouldRefreshDBStateCorrectlyWhenNeverCompacted(t *testing.T) {
	bucket, sm, state := buildTestState()
	db, err := Open(testPath, bucket)
	assert.NoError(t, err)
	db.Put(repeatedChar('a', 16), repeatedChar('b', 48))
	db.Put(repeatedChar('j', 16), repeatedChar('k', 48))

	writerDBState := waitForManifestWithL0Len(sm, len(state.dbState.l0)+1)

	state.refreshDBState(writerDBState)

	assert.True(t, state.dbState.l0LastCompacted.IsAbsent())
	for i := 0; i < len(writerDBState.l0); i++ {
		assert.Equal(t, writerDBState.l0[i].id.compactedID(), state.dbState.l0[i].id.compactedID())
	}
}

func TestShouldRefreshDBStateCorrectly(t *testing.T) {
	bucket, sm, state := buildTestState()
	originalL0s := state.dbState.clone().l0
	compactedID, ok := originalL0s[len(originalL0s)-1].id.compactedID().Get()
	assert.True(t, ok)
	compaction := newCompaction([]SourceID{newSourceIDSST(compactedID)}, 0)
	err := state.submitCompaction(compaction)
	assert.NoError(t, err)
	state.finishCompaction(&SortedRun{
		id:      0,
		sstList: []SSTableHandle{originalL0s[len(originalL0s)-1]},
	})

	db, err := Open(testPath, bucket)
	assert.NoError(t, err)
	db.Put(repeatedChar('a', 16), repeatedChar('b', 48))
	db.Put(repeatedChar('j', 16), repeatedChar('k', 48))
	writerDBState := waitForManifestWithL0Len(sm, len(originalL0s)+1)
	dbStateBeforeMerge := state.dbState.clone()

	state.refreshDBState(writerDBState)

	dbState := state.dbState
	// last sst was removed during compaction
	expectedMergedL0s := originalL0s[:len(originalL0s)-1]
	// new sst got added during db.Put() call above
	expectedMergedL0s = append([]SSTableHandle{writerDBState.l0[0]}, expectedMergedL0s...)
	for i := 0; i < len(expectedMergedL0s); i++ {
		expected, _ := expectedMergedL0s[i].id.compactedID().Get()
		actual, _ := dbState.l0[i].id.compactedID().Get()
		assert.Equal(t, expected, actual)
	}
	for i := 0; i < len(dbStateBeforeMerge.compacted); i++ {
		srBefore := dbStateBeforeMerge.compacted[i]
		srAfter := dbState.compacted[i]
		assert.Equal(t, srBefore.id, srAfter.id)
		for j := 0; j < len(srBefore.sstList); j++ {
			assert.Equal(t, srBefore.sstList[j].id, srAfter.sstList[j].id)
		}
	}
	assert.Equal(t, writerDBState.lastCompactedWalSSTID, dbState.lastCompactedWalSSTID)
	assert.Equal(t, writerDBState.nextWalSstID, dbState.nextWalSstID)
}

func TestShouldRefreshDBStateCorrectlyWhenAllL0Compacted(t *testing.T) {
	bucket, sm, state := buildTestState()
	originalL0s := state.dbState.clone().l0

	sourceIDs := make([]SourceID, 0)
	for _, sst := range originalL0s {
		id, ok := sst.id.compactedID().Get()
		assert.True(t, ok)
		sourceIDs = append(sourceIDs, newSourceIDSST(id))
	}
	compaction := newCompaction(sourceIDs, 0)
	err := state.submitCompaction(compaction)
	assert.NoError(t, err)
	state.finishCompaction(&SortedRun{
		id:      0,
		sstList: originalL0s,
	})
	assert.Equal(t, 0, len(state.dbState.l0))

	db, err := Open(testPath, bucket)
	assert.NoError(t, err)
	db.Put(repeatedChar('a', 16), repeatedChar('b', 48))
	db.Put(repeatedChar('j', 16), repeatedChar('k', 48))
	writerDBState := waitForManifestWithL0Len(sm, len(originalL0s)+1)

	state.refreshDBState(writerDBState)

	dbState := state.dbState
	assert.Equal(t, 1, len(dbState.l0))
	expectedID, _ := writerDBState.l0[0].id.compactedID().Get()
	actualID, _ := dbState.l0[0].id.compactedID().Get()
	assert.Equal(t, expectedID, actualID)
}

func waitForManifestWithL0Len(storedManifest StoredManifest, size int) *CoreDBState {
	startTime := time.Now()
	for time.Since(startTime) < time.Second*30 {
		dbState, err := storedManifest.refresh()
		common.AssertTrue(err == nil, "")
		if len(dbState.l0) == size {
			return dbState.clone()
		}
		time.Sleep(time.Millisecond * 100)
	}
	panic("no manifest found with l0 len")
}

func buildL0Compaction(sstList []SSTableHandle, destination uint32) Compaction {
	sources := make([]SourceID, 0)
	for _, sst := range sstList {
		id, ok := sst.id.compactedID().Get()
		common.AssertTrue(ok, "expected compacted SST ID")
		sources = append(sources, newSourceIDSST(id))
	}
	return newCompaction(sources, destination)
}

func buildTestState() (objstore.Bucket, StoredManifest, *CompactorState) {
	bucket := objstore.NewInMemBucket()
	db, err := Open(testPath, bucket)
	common.AssertTrue(err == nil, "Could not open db")
	l0Count := 5
	for i := 0; i < l0Count; i++ {
		db.Put(repeatedChar(rune('a'+i), 16), repeatedChar(rune('b'+i), 48))
		db.Put(repeatedChar(rune('j'+i), 16), repeatedChar(rune('k'+i), 48))
	}
	db.Close()

	manifestStore := newManifestStore(testPath, bucket)
	sm, err := loadStoredManifest(manifestStore)
	common.AssertTrue(err == nil, "Could not load stored manifest")
	common.AssertTrue(sm.IsPresent(), "Could not find stored manifest")
	storedManifest, _ := sm.Get()
	state := newCompactorState(storedManifest.dbState())
	return bucket, storedManifest, state
}
