package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/mocachain/moca-common/go/hash"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsppieceop"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	metatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	st "github.com/mocachain/moca/v2/x/storage/types"
	vg "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRecoveryFenceSourcesOnlyWriteStaging(t *testing.T) {
	for _, source := range []string{"primary", "secondary-primary", "secondary-ec"} {
		for _, successor := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/successor=%t", source, successor), func(t *testing.T) {
				e := setup(t)
				ctrl := gomock.NewController(t)
				data := []byte("body")
				checksum := hash.GenerateChecksum(data)
				root := hash.GenerateIntegrityHash([][]byte{checksum})
				task := &gfsptask.GfSpRecoverPieceTask{Task: &gfsptask.GfSpTask{}, ObjectInfo: &st.ObjectInfo{Id: sdkmath.NewUint(81), PayloadSize: 4, Version: 2, ObjectStatus: st.OBJECT_STATUS_SEALED, Checksums: [][]byte{root, root}}, StorageParams: &st.Params{VersionedParams: st.VersionedParams{MaxSegmentSize: 4, RedundantDataChunkNum: 1}}, EcIdx: 0, BySuccessorSp: successor}
				if source == "secondary-primary" {
					task.EcIdx = -1
				}
				task.SetRecoveryFence(bytes.Repeat([]byte{0x61}, 32))
				chain := consensus.NewMockConsensus(ctrl)
				chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "81").Return(task.ObjectInfo, nil).AnyTimes()
				chain.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(0)).Return(task.StorageParams, nil).AnyTimes()
				chain.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{{Id: 1, Endpoint: "source"}}, nil)
				client := gfspclient.NewMockGfSpClientAPI(ctrl)
				if source == "primary" {
					chain.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&vg.GlobalVirtualGroupFamily{PrimarySpId: 1}, nil)
					client.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metatypes.VGFInfoBucket{BucketInfo: &st.BucketInfo{Id: sdkmath.NewUint(1)}}, nil, nil)
				} else {
					client.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(&metatypes.Bucket{BucketInfo: &st.BucketInfo{Id: sdkmath.NewUint(1)}}, nil)
					client.EXPECT().GetGlobalVirtualGroup(gomock.Any(), uint64(1), uint32(0)).Return(&vg.GlobalVirtualGroup{SecondarySpIds: []uint32{1}}, nil)
				}
				client.EXPECT().SignRecoveryTask(gomock.Any(), gomock.Any()).Return([]byte("sig"), nil)
				client.EXPECT().GetPieceFromECChunks(gomock.Any(), "source", gomock.Any()).Return(io.NopCloser(bytes.NewReader(data)), nil)
				e.baseApp.SetConsensus(chain)
				e.baseApp.SetGfSpClient(client)
				e.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
				db := spdb.NewMockSPDB(ctrl)
				if source == "secondary-ec" {
					db.EXPECT().GetSpByAddress(gomock.Any(), spdb.OperatorAddressType).Return(&sptypes.StorageProvider{Endpoint: "self"}, nil)
				}
				if !successor {
					db.EXPECT().GetObjectIntegrity(uint64(81), task.EcIdx).Return(&spdb.IntegrityMeta{PieceChecksumList: [][]byte{checksum}}, nil)
				}
				e.baseApp.SetGfSpDB(db) // No executor metadata writes or temporary checksum calls are permitted.
				store := piecestore.NewMockPieceStore(ctrl)
				key, err := gfsptask.RecoveryStageKey(task)
				require.NoError(t, err)
				store.EXPECT().PutPiece(gomock.Any(), key, data).Return(nil)
				e.baseApp.SetPieceStore(store)
				if source == "primary" {
					require.NoError(t, e.recoverByPrimarySP(context.Background(), task))
				} else {
					require.NoError(t, e.recoverBySecondarySP(context.Background(), task, source == "secondary-ec"))
				}
			})
		}
	}
}

func TestRecoveryFenceRejectsChangedChainBeforeReadingSources(t *testing.T) {
	for _, change := range []string{"version", "size", "updating", "checksum", "historical params"} {
		t.Run(change, func(t *testing.T) {
			e := setup(t)
			ctrl := gomock.NewController(t)
			object := &st.ObjectInfo{Id: sdkmath.NewUint(81), PayloadSize: 4, Version: 2, ObjectStatus: st.OBJECT_STATUS_SEALED, Checksums: [][]byte{[]byte("root"), []byte("root")}}
			params := &st.Params{VersionedParams: st.VersionedParams{MaxSegmentSize: 4, RedundantDataChunkNum: 1}}
			task := &gfsptask.GfSpRecoverPieceTask{Task: &gfsptask.GfSpTask{}, ObjectInfo: object, StorageParams: params, RecoveryFence: bytes.Repeat([]byte{1}, 32)}
			current := *object
			switch change {
			case "version":
				current.Version++
			case "size":
				current.PayloadSize++
			case "updating":
				current.IsUpdating = true
			case "checksum":
				current.Checksums = [][]byte{[]byte("changed"), []byte("changed")}
			}
			chain := consensus.NewMockConsensus(ctrl)
			chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "81").Return(&current, nil).AnyTimes()
			if change == "historical params" {
				chain.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(0)).Return(&st.Params{VersionedParams: st.VersionedParams{MaxSegmentSize: 8, RedundantDataChunkNum: 1}}, nil).AnyTimes()
			}
			e.baseApp.SetConsensus(chain)
			e.baseApp.SetPieceStore(piecestore.NewMockPieceStore(ctrl))
			e.baseApp.SetGfSpClient(gfspclient.NewMockGfSpClientAPI(ctrl))
			e.baseApp.SetGfSpDB(spdb.NewMockSPDB(ctrl))
			require.Error(t, e.recoverByPrimarySP(context.Background(), task))
			require.Error(t, e.recoverBySecondarySP(context.Background(), task, true))
		})
	}
}

func TestRecoveryFenceSourceResponseCannotExceedStagingReservation(t *testing.T) {
	e := setup(t)
	ctrl := gomock.NewController(t)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	client.EXPECT().GetPieceFromECChunks(gomock.Any(), "source", gomock.Any()).Return(io.NopCloser(bytes.NewReader([]byte("oversize"))), nil)
	e.baseApp.SetGfSpClient(client)
	task := &gfsptask.GfSpRecoverPieceTask{Task: &gfsptask.GfSpTask{}, ObjectInfo: &st.ObjectInfo{Id: sdkmath.NewUint(1), PayloadSize: 4}, StorageParams: &st.Params{VersionedParams: st.VersionedParams{MaxSegmentSize: 4, RedundantDataChunkNum: 1}}}
	data, err := e.doRecoveryPiece(context.Background(), task, "source")
	require.Error(t, err)
	require.Nil(t, data)
}
