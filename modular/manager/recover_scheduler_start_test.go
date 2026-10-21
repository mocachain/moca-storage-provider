package manager

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	sdkmath "cosmossdk.io/math"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	metadatatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
)

// All scheduler goroutines and their clock live inside the synctest bubble.
// DB reads and writes copy values to model persistence rather than pointer aliasing.
func TestRecoverGVGSchedulerStart_RetainsBatchAfterFailedRecordInsert(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := setup(t)
		ctrl := gomock.NewController(t)
		db := spdb.NewMockSPDB(ctrl)
		con := consensus.NewMockConsensus(ctrl)
		client := gfspclient.NewMockGfSpClientAPI(ctrl)
		m.baseApp.SetGfSpDB(db)
		m.baseApp.SetConsensus(con)
		m.baseApp.SetGfSpClient(client)
		m.recoveryQueue = gfsptqueue.NewGfSpTQueueWithLimit("recovery", 10)
		m.recoverObjectStats = NewObjectsSegmentsStats()
		params := &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 10}}
		con.EXPECT().QueryStorageParams(gomock.Any()).Return(params, nil)
		persisted := spdb.RecoverGVGStats{VirtualGroupID: 1, StartAfter: 10, NextStartAfter: 100, Limit: 10, Status: spdb.Processing}
		db.EXPECT().GetRecoverGVGStats(uint32(1)).DoAndReturn(func(uint32) (*spdb.RecoverGVGStats, error) { copy := persisted; return &copy, nil }).AnyTimes()
		db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).DoAndReturn(func(stats *spdb.RecoverGVGStats) error { persisted = *stats; return nil }).AnyTimes()
		var cursors []uint64
		client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(1), gomock.Any(), uint32(10)).DoAndReturn(func(_ context.Context, _ uint32, cursor uint64, _ uint32, _ ...grpc.DialOption) ([]*metadatatypes.ObjectDetails, error) {
			cursors = append(cursors, cursor)
			if cursor == 100 {
				return nil, nil
			}
			return []*metadatatypes.ObjectDetails{{Object: &metadatatypes.Object{ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), Version: 2, PayloadSize: 1}}, Gvg: &virtualgrouptypes.GlobalVirtualGroup{Id: 1}}}, nil
		}).AnyTimes()
		inserts := 0
		db.EXPECT().InsertRecoverFailedObject(gomock.Any()).DoAndReturn(func(o *spdb.RecoverFailedObject) error {
			assert.Equal(t, uint64(100), o.ObjectID)
			inserts++
			if inserts == 1 {
				return errors.New("temporary database failure")
			}
			return nil
		}).AnyTimes()
		scheduler := &RecoverGVGScheduler{manager: m, gvgID: 1, currentBatchObjectIDs: make(map[objectVersion]struct{})}
		done := make(chan struct{})
		go func() { scheduler.Start(); close(done) }()
		synctest.Wait()
		// Start lists at 10s; monitoring times out strictly after another 120s.
		time.Sleep(140 * time.Second)
		synctest.Wait()
		assert.Equal(t, 1, inserts)
		assert.Equal(t, uint64(10), persisted.StartAfter, "insert failure must retain the persisted cursor")
		assert.Equal(t, []uint64{10}, cursors, "must not list a subsequent batch after an insert failure")
		time.Sleep(20 * time.Second)
		synctest.Wait()
		assert.Equal(t, 2, inserts, "the same object must be retried")
		assert.Equal(t, uint64(100), persisted.StartAfter)
		assert.Equal(t, []uint64{10, 100}, cursors)
		<-done
	})
}
