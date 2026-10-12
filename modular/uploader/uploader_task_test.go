package uploader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"

	"github.com/mocachain/moca-common/go/hash"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	corespdb "github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	"github.com/mocachain/moca-storage-provider/core/taskqueue"
	servicetypes "github.com/mocachain/moca-storage-provider/store/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

func TestUploadModular_PreUploadObject(t *testing.T) {
	cases := []struct {
		name             string
		uploadObjectTask coretask.UploadObjectTask
		wantedErr        error
	}{
		{
			name:             "1",
			uploadObjectTask: nil,
			wantedErr:        ErrDanglingUploadTask,
		},
		{
			name: "2",
			uploadObjectTask: &gfsptask.GfSpUploadObjectTask{
				ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_DISCONTINUED},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
			},
			wantedErr: ErrNotCreatedState,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			u := setup(t)
			err := u.PreUploadObject(context.TODO(), tt.uploadObjectTask)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestUploadModular_PreUploadObjectSuccess(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_UPLOAD_OBJECT_DOING), "", nil).Times(1)

	m1.EXPECT().CreateUploadObject(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Nil(t, err)
}

func TestUploadModular_PreUploadObjectFailure1(t *testing.T) {
	t.Log("Failure case description: task repeated")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(true).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrRepeatedTask, err)
}

func TestUploadModular_PreUploadObjectFailure2(t *testing.T) {
	t.Log("Failure case description: failed to begin upload object task")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().CreateUploadObject(gomock.Any(), gomock.Any()).Return(mockErr).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, mockErr, err)
}

func TestUploadModular_PreUploadObjectFailure3(t *testing.T) {
	t.Log("Can't proceed for object which had been fully uploaded")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_UPLOAD_OBJECT_DONE), "", nil).Times(1)
	m1.EXPECT().CreateUploadObject(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrInvalidUploadRequest, err)
}

func TestUploadModular_PreUploadObjectFailure4(t *testing.T) {
	t.Log("failed to get upload object state")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_INIT_UNSPECIFIED), "", mockErr).Times(1)
	m1.EXPECT().CreateUploadObject(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrGetObjectUploadState, err)
}

func TestUploadModular_PreUploadObjectFailure5(t *testing.T) {
	t.Log("Can't proceed for object which had been fully uploaded")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_REPLICATE_OBJECT_DOING), "", nil).Times(1)
	m1.EXPECT().CreateUploadObject(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrInvalidUploadRequest, err)
}

func TestUploadModular_HandleUploadObjectTaskSuccess1(t *testing.T) {
	t.Log("Success case description: succeed to upload payload to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m3)
	m3.EXPECT().SetObjectIntegrity(gomock.Any()).Return(nil).AnyTimes()
	m3.EXPECT().UpdateUploadProgress(gomock.Any()).Return(nil).AnyTimes()

	m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m4)
	m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
			Checksums: [][]byte{{
				227, 176, 196, 66, 152, 252, 28, 20, 154, 251, 244, 200, 153, 111, 185, 36, 39, 174, 65,
				228, 100, 155, 147, 76, 164, 149, 153, 27, 120, 82, 184, 85,
			}},
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Nil(t, err)
}

func TestUploadModular_HandleUploadObjectForUpdateTaskSuccess1(t *testing.T) {
	t.Log("Success case description: succeed to upload payload to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m3)
	m3.EXPECT().SetShadowObjectIntegrity(gomock.Any()).Return(nil).AnyTimes()
	m3.EXPECT().UpdateUploadProgress(gomock.Any()).Return(nil).AnyTimes()

	m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m4)
	m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
			Checksums: [][]byte{{
				227, 176, 196, 66, 152, 252, 28, 20, 154, 251, 244, 200, 153, 111, 185, 36, 39, 174, 65,
				228, 100, 155, 147, 76, 164, 149, 153, 27, 120, 82, 184, 85,
			}},
			IsUpdating: true,
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Nil(t, err)
}

func TestUploadModular_HandleUploadObjectTaskFailure1(t *testing.T) {
	t.Log("Failure case description: failed to push upload queue")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().Push(gomock.Any()).Return(mockErr).Times(1)
	m.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	err := u.HandleUploadObjectTask(context.TODO(), &gfsptask.GfSpUploadObjectTask{}, nil)
	assert.Equal(t, mockErr, err)
}

func TestUploadModular_HandleUploadObjectTaskFailure2(t *testing.T) {
	t.Log("Failure case description: failed to put segment piece to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	u.baseApp.SetPieceStore(m4)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task:          &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleUploadObjectTaskFailure3(t *testing.T) {
	t.Log("Failure case description: invalid integrity hash")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task:          &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo:    &storagetypes.ObjectInfo{Checksums: [][]byte{[]byte("test")}},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Equal(t, ErrInvalidIntegrity, err)
}

func TestUploadModular_HandleUploadObjectTaskFailure4(t *testing.T) {
	t.Log("Failure case description: failed to write integrity hash to db")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m3)
	m3.EXPECT().SetObjectIntegrity(gomock.Any()).Return(mockErr).AnyTimes()

	m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m4)
	m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
			Checksums: [][]byte{{
				227, 176, 196, 66, 152, 252, 28, 20, 154, 251, 244, 200, 153, 111, 185, 36, 39, 174, 65,
				228, 100, 155, 147, 76, 164, 149, 153, 27, 120, 82, 184, 85,
			}},
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleUploadObjectTaskFailure5(t *testing.T) {
	t.Log("Failure case description: stream closed abnormally")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, mockErr).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:        sdkmath.NewUint(1),
			Checksums: [][]byte{{227, 176, 196, 66, 152, 252, 28, 20, 154, 251, 244, 200, 153, 111, 185, 36, 39, 174, 65, 228, 100, 155, 147, 76, 164, 149, 153, 27, 120, 82, 184, 85}},
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Equal(t, ErrClosedStream, err)
}

func TestUploadModular_HandleUploadObjectTaskFailure6(t *testing.T) {
	t.Log("Failure case description: failed to put segment piece to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, nil).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	u.baseApp.SetPieceStore(m4)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()

	uploadObjectTask := &gfsptask.GfSpUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleUploadObjectTask(context.TODO(), uploadObjectTask, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_PreResumableUploadObject(t *testing.T) {
	cases := []struct {
		name             string
		uploadObjectTask coretask.ResumableUploadObjectTask
		wantedErr        error
	}{
		{
			name:             "1",
			uploadObjectTask: nil,
			wantedErr:        ErrDanglingUploadTask,
		},
		{
			name: "2",
			uploadObjectTask: &gfsptask.GfSpResumableUploadObjectTask{
				ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_DISCONTINUED},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
			},
			wantedErr: ErrNotCreatedState,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			u := setup(t)
			err := u.PreResumableUploadObject(context.TODO(), tt.uploadObjectTask)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestUploadModular_PreResumableUploadObjectSuccess(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().CreateResumableUploadObject(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_UPLOAD_OBJECT_DOING), "", nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Nil(t, err)
}

func TestUploadModular_PreResumableUploadObjectFailure1(t *testing.T) {
	t.Log("Failure case description: task repeated")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(true).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrRepeatedTask, err)
}

func TestUploadModular_PreResumableUploadObjectFailure2(t *testing.T) {
	t.Log("Failure case description: failed to begin upload object task")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_INIT_UNSPECIFIED), "", nil)
	m1.EXPECT().CreateResumableUploadObject(gomock.Any(), gomock.Any()).Return(mockErr).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, mockErr, err)
}

func TestUploadModular_PreResumableUploadObjectFailure3(t *testing.T) {
	t.Log("Can't proceed for object which had been fully uploaded")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_UPLOAD_OBJECT_DONE), "", nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrInvalidUploadRequest, err)
}

func TestUploadModular_PreResumableUploadObjectFailure4(t *testing.T) {
	t.Log("failed to get upload object state")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_INIT_UNSPECIFIED), "", mockErr).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrGetObjectUploadState, err)
}

func TestUploadModular_PreResumableUploadObjectFailure5(t *testing.T) {
	t.Log("Can't proceed for object which had been fully uploaded")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Has(gomock.Any()).Return(false).Times(1)

	m1 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m1)
	m1.EXPECT().GetUploadObjectState(gomock.Any(), gomock.Any()).Return(int32(servicetypes.TaskState_TASK_STATE_REPLICATE_OBJECT_DOING), "", nil).Times(1)

	uploadObjectTask := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo:    &storagetypes.ObjectInfo{ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{MaxPayloadSize: 1},
	}
	err := u.PreResumableUploadObject(context.TODO(), uploadObjectTask)
	assert.Equal(t, ErrInvalidUploadRequest, err)
}

func TestUploadModular_validateResumableUploadOffset(t *testing.T) {
	tests := []struct {
		name          string
		offset        uint64
		payloadSize   uint64
		segmentSize   uint64
		persistedSize uint64
		queryErr      error
		wantErr       error
	}{
		{
			name:        "first upload starts at zero",
			payloadSize: 32,
			segmentSize: 8,
			queryErr:    gorm.ErrRecordNotFound,
		},
		{
			name:        "first upload rejects a non-zero offset",
			offset:      8,
			payloadSize: 32,
			segmentSize: 8,
			queryErr:    gorm.ErrRecordNotFound,
			wantErr:     ErrInvalidResumeOffset,
		},
		{
			name:          "resume matches persisted progress",
			offset:        16,
			payloadSize:   32,
			segmentSize:   8,
			persistedSize: 16,
		},
		{
			name:          "resume cannot skip persisted progress",
			offset:        24,
			payloadSize:   32,
			segmentSize:   8,
			persistedSize: 16,
			wantErr:       ErrInvalidResumeOffset,
		},
		{
			name:          "resume cannot overwrite persisted progress",
			offset:        8,
			payloadSize:   32,
			segmentSize:   8,
			persistedSize: 16,
			wantErr:       ErrInvalidResumeOffset,
		},
		{
			name:          "resume offset must be segment aligned",
			offset:        15,
			payloadSize:   32,
			segmentSize:   8,
			persistedSize: 15,
			wantErr:       ErrInvalidResumeOffset,
		},
		{
			name:          "resume offset cannot reach payload end",
			offset:        32,
			payloadSize:   32,
			segmentSize:   8,
			persistedSize: 32,
			wantErr:       ErrInvalidResumeOffset,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := setup(t)
			ctrl := gomock.NewController(t)
			db := corespdb.NewMockSPDB(ctrl)
			u.baseApp.SetGfSpDB(db)

			db.EXPECT().GetObjectIntegrity(uint64(1), int32(piecestore.PrimarySPRedundancyIndex)).
				Return(&corespdb.IntegrityMeta{ObjectSize: tt.persistedSize}, tt.queryErr).AnyTimes()

			task := &gfsptask.GfSpResumableUploadObjectTask{
				ObjectInfo: &storagetypes.ObjectInfo{
					Id:          sdkmath.NewUint(1),
					PayloadSize: tt.payloadSize,
				},
				StorageParams: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: tt.segmentSize}},
				Offset:        tt.offset,
			}

			err := u.validateResumableUploadOffset(task)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestUploadModular_validateResumableUploadOffsetUsesShadowProgressForUpdate(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)
	db := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(db)
	db.EXPECT().GetShadowObjectIntegrity(uint64(1), int32(piecestore.PrimarySPRedundancyIndex)).
		Return(&corespdb.ShadowIntegrityMeta{ObjectSize: 16}, nil)

	task := &gfsptask.GfSpResumableUploadObjectTask{
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:          sdkmath.NewUint(1),
			PayloadSize: 32,
			IsUpdating:  true,
		},
		StorageParams: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 8}},
		Offset:        16,
	}

	assert.NoError(t, u.validateResumableUploadOffset(task))
}

func TestUploadModular_HandleResumableUploadObjectTaskSuccess(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m3)
	m3.EXPECT().SetObjectIntegrity(gomock.Any()).Return(nil).AnyTimes()

	m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m4)
	m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Nil(t, err)
}

func TestUploadModular_HandleResumableUploadObjectForUpdateTaskSuccess(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m3)
	m3.EXPECT().SetShadowObjectIntegrity(gomock.Any()).Return(nil).AnyTimes()

	m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m4)
	m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:         sdkmath.NewUint(1),
			IsUpdating: true,
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Nil(t, err)
}

// TestUploadModular_HandleResumableUploadObjectTaskRejectsPayloadSizeMismatch
// covers agent and non-agent uploads whose stored pieces don't add up to the object's
// declared payload size: the handler must reject synchronously (matching how
// HandleUploadObjectTask's identical check already behaves), not fall through
// to mark the upload done while a separate goroutine asks the chain to reject
// the object. No SetObjectIntegrity/UpdateUploadProgress expectation is
// registered below, so gomock fails loudly if the handler still reaches them.
func TestUploadModular_HandleResumableUploadObjectTaskRejectsPayloadSizeMismatch(t *testing.T) {
	for _, isAgent := range []bool{true, false} {
		t.Run(fmt.Sprintf("agent=%t", isAgent), func(t *testing.T) {
			u := setup(t)
			ctrl := gomock.NewController(t)

			m := gfspclient.NewMockstdLib(ctrl)
			m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

			m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
			u.resumeableUploadQueue = m1
			m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
			m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

			m2 := piecestore.NewMockPieceOp(ctrl)
			u.baseApp.SetPieceOp(m2)
			m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

			m3 := corespdb.NewMockSPDB(ctrl)
			u.baseApp.SetGfSpDB(m3)
			// the stored pieces only total 5 bytes, but the object claims a 100-byte payload
			m3.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
				ObjectSize: 5,
			}, nil).Times(1)

			m4 := gfspclient.NewMockGfSpClientAPI(ctrl)
			u.baseApp.SetGfSpClient(m4)
			m4.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			// the async on-chain reject (unchanged by this fix) - let it succeed
			// immediately so the background goroutine it runs in exits cleanly.
			m4.EXPECT().RejectUnSealObject(gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()
			m5 := consensus.NewMockConsensus(ctrl)
			u.baseApp.SetConsensus(m5)
			m5.EXPECT().ListenRejectUnSealObject(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).AnyTimes()

			task := &gfsptask.GfSpResumableUploadObjectTask{
				Task: &gfsptask.GfSpTask{TaskPriority: 1},
				ObjectInfo: &storagetypes.ObjectInfo{
					Id:          sdkmath.NewUint(1),
					PayloadSize: 100,
					Checksums:   [][]byte{hash.GenerateIntegrityHash(nil)},
				},
				StorageParams: &storagetypes.Params{},
				Completed:     true,
				IsAgentUpload: isAgent,
			}
			err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
			assert.ErrorIs(t, err, ErrPayloadSize)

		})
	}
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure1(t *testing.T) {
	t.Log("Failure case description: failed to push upload queue")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m
	m.EXPECT().Push(gomock.Any()).Return(mockErr).Times(1)
	m.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	err := u.HandleResumableUploadObjectTask(context.TODO(), &gfsptask.GfSpResumableUploadObjectTask{}, nil)
	assert.Equal(t, mockErr, err)
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure2(t *testing.T) {
	t.Log("Failure case description: failed to put segment piece to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()
	u.baseApp.SetPieceStore(m4)

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure3(t *testing.T) {
	t.Log("Failure case description: failed to append integrity checksum to db")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	u.baseApp.SetPieceStore(m4)

	m5 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m5)
	m5.EXPECT().UpdatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure4(t *testing.T) {
	t.Log("Failure case description: failed to get object integrity hash")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m4)
	m4.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil, mockErr).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
		Completed:     true,
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Equal(t, err, mockErr)
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure5(t *testing.T) {
	t.Log("Failure case description: invalid integrity hash")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m4)
	m4.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
		PieceChecksumList: [][]byte{[]byte("test")},
	}, nil).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:        sdkmath.NewUint(1),
			Checksums: [][]byte{[]byte("mock")},
		},
		StorageParams: &storagetypes.Params{},
		Completed:     true,
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Equal(t, ErrInvalidIntegrity, err)
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure6(t *testing.T) {
	t.Log("Failure case description: failed to write integrity hash to db")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m4)
	m4.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
		PieceChecksumList: [][]byte{[]byte("test")},
	}, nil).AnyTimes()
	m4.EXPECT().UpdateIntegrityChecksum(gomock.Any()).Return(mockErr).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
			Checksums: [][]byte{{
				159, 134, 208, 129, 136, 76, 125, 101, 154, 47, 234, 160, 197, 90, 208, 21, 163, 191,
				79, 27, 43, 11, 130, 44, 209, 93, 108, 21, 176, 240, 10, 8,
			}},
		},
		StorageParams: &storagetypes.Params{},
		Completed:     true,
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure7(t *testing.T) {
	t.Log("Failure case description: stream closed abnormally")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(0, mockErr).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Equal(t, ErrClosedStream, err)
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure8(t *testing.T) {
	t.Log("Failure case description: failed to put segment piece to piece store")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, nil).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	u.baseApp.SetPieceStore(m4)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_HandleResumableUploadObjectTaskFailure9(t *testing.T) {
	t.Log("Failure case description: failed to append integrity checksum to db")
	u := setup(t)
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockstdLib(ctrl)
	m.EXPECT().Read(gomock.Any()).Return(1, nil).AnyTimes()

	m1 := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.resumeableUploadQueue = m1
	m1.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	m1.EXPECT().PopByKey(gomock.Any()).Return(&gfsptask.GfSpResumableUploadObjectTask{}).AnyTimes()

	m2 := piecestore.NewMockPieceOp(ctrl)
	u.baseApp.SetPieceOp(m2)
	m2.EXPECT().MaxSegmentPieceSize(gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
	m2.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()

	m3 := gfspclient.NewMockGfSpClientAPI(ctrl)
	u.baseApp.SetGfSpClient(m3)
	m3.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m4 := piecestore.NewMockPieceStore(ctrl)
	u.baseApp.SetPieceStore(m4)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	m5 := corespdb.NewMockSPDB(ctrl)
	u.baseApp.SetGfSpDB(m5)
	m5.EXPECT().UpdatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(mockErr).AnyTimes()

	task := &gfsptask.GfSpResumableUploadObjectTask{
		Task: &gfsptask.GfSpTask{TaskPriority: 1},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(1),
		},
		StorageParams: &storagetypes.Params{},
	}
	err := u.HandleResumableUploadObjectTask(context.TODO(), task, m)
	assert.Contains(t, err.Error(), mockErr.Error())
}

func TestUploadModular_PostUploadObject(t *testing.T) {
	u := setup(t)
	u.PostUploadObject(context.TODO(), &gfsptask.GfSpUploadObjectTask{})
}

func TestUploadModular_QueryTasks(t *testing.T) {
	u := setup(t)
	ctrl := gomock.NewController(t)

	m := taskqueue.NewMockTQueueOnStrategy(ctrl)
	u.uploadQueue = m
	m.EXPECT().ScanTask(gomock.Any()).AnyTimes()

	result, err := u.QueryTasks(context.TODO(), "111")
	assert.Nil(t, err)
	assert.Equal(t, 0, len(result))
}

func TestUploadModular_PostResumableUploadObject(t *testing.T) {
	u := setup(t)
	u.PostResumableUploadObject(context.TODO(), &gfsptask.GfSpResumableUploadObjectTask{})
}

func TestStreamReadAt(t *testing.T) {
	cases := []struct {
		name         string
		stream       io.Reader
		b            []byte
		wantedResult int
		wantedErr    error
	}{
		{
			name:         "1",
			stream:       nil,
			b:            nil,
			wantedResult: 0,
			wantedErr:    errors.New(("failed to read due to invalid args")),
		},
		{
			name:         "2",
			stream:       strings.NewReader("mock"),
			b:            []byte("test"),
			wantedResult: 4,
			wantedErr:    nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := StreamReadAt(tt.stream, tt.b)
			assert.Equal(t, tt.wantedResult, result)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestPreResumableUploadChecksFinishedStateBeforeOffset(t *testing.T) {
	for _, state := range []servicetypes.TaskState{servicetypes.TaskState_TASK_STATE_UPLOAD_OBJECT_DONE, servicetypes.TaskState_TASK_STATE_REPLICATE_OBJECT_DOING} {
		t.Run(state.String(), func(t *testing.T) {
			u := setup(t)
			ctrl := gomock.NewController(t)
			queue := taskqueue.NewMockTQueueOnStrategy(ctrl)
			u.resumeableUploadQueue = queue
			queue.EXPECT().Has(gomock.Any()).Return(false)
			client := gfspclient.NewMockGfSpClientAPI(ctrl)
			u.baseApp.SetGfSpClient(client)
			client.EXPECT().GetUploadObjectState(gomock.Any(), uint64(1)).Return(int32(state), "", nil)
			// Neither integrity lookup nor manager notification is allowed for finished uploads.
			u.baseApp.SetGfSpDB(corespdb.NewMockSPDB(ctrl))
			task := &gfsptask.GfSpResumableUploadObjectTask{
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1), ObjectStatus: storagetypes.OBJECT_STATUS_CREATED, PayloadSize: 16},
				StorageParams: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 8}}, Offset: 16,
			}
			require.ErrorIs(t, u.PreResumableUploadObject(context.Background(), task), ErrInvalidUploadRequest)
		})
	}
}

func TestResumableUploadDiscardsPartialSegmentAndCanResume(t *testing.T) {
	for _, updating := range []bool{false, true} {
		t.Run(fmt.Sprintf("updating=%t", updating), func(t *testing.T) {
			u := setup(t)
			ctrl := gomock.NewController(t)
			queue := taskqueue.NewMockTQueueOnStrategy(ctrl)
			u.resumeableUploadQueue = queue
			queue.EXPECT().Push(gomock.Any()).Return(nil).Times(2)
			queue.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(2)
			client := gfspclient.NewMockGfSpClientAPI(ctrl)
			u.baseApp.SetGfSpClient(client)
			reported := make(chan struct{}, 2)
			client.EXPECT().ReportTask(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, coretask.Task) error { reported <- struct{}{}; return nil }).Times(2)
			op := piecestore.NewMockPieceOp(ctrl)
			u.baseApp.SetPieceOp(op)
			op.EXPECT().MaxSegmentPieceSize(uint64(19), uint64(8)).Return(int64(8)).Times(2)
			op.EXPECT().SegmentPieceKey(uint64(1), uint32(0), int64(0)).Return("segment0")
			op.EXPECT().SegmentPieceKey(uint64(1), uint32(1), int64(0)).Return("segment1")
			op.EXPECT().SegmentPieceKey(uint64(1), uint32(2), int64(0)).Return("segment2")
			pieces := piecestore.NewMockPieceStore(ctrl)
			u.baseApp.SetPieceStore(pieces)
			pieces.EXPECT().PutPiece(gomock.Any(), "segment0", []byte("abcdefgh")).Return(nil)
			pieces.EXPECT().PutPiece(gomock.Any(), "segment1", []byte("ijklmnop")).Return(nil)
			pieces.EXPECT().PutPiece(gomock.Any(), "segment2", []byte("qrs")).Return(nil)
			db := corespdb.NewMockSPDB(ctrl)
			u.baseApp.SetGfSpDB(db)
			db.EXPECT().UpdateUploadProgress(gomock.Any()).Return(nil)
			if updating {
				db.EXPECT().UpdateShadowPieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("abcdefgh")), int64(0), uint64(8)).Return(nil)
				db.EXPECT().UpdateShadowPieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("ijklmnop")), int64(0), uint64(8)).Return(nil)
				db.EXPECT().GetShadowObjectIntegrity(uint64(1), int32(-1)).Return(&corespdb.ShadowIntegrityMeta{ObjectSize: 8}, nil)
				db.EXPECT().UpdateShadowPieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("qrs")), int64(0), uint64(3)).Return(nil)
				db.EXPECT().GetShadowObjectIntegrity(uint64(1), int32(-1)).Return(&corespdb.ShadowIntegrityMeta{ObjectSize: 19}, nil)
				db.EXPECT().UpdateShadowIntegrityChecksum(gomock.Any()).Return(nil)
			} else {
				db.EXPECT().UpdatePieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("abcdefgh")), uint64(8)).Return(nil)
				db.EXPECT().UpdatePieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("ijklmnop")), uint64(8)).Return(nil)
				db.EXPECT().GetObjectIntegrity(uint64(1), int32(-1)).Return(&corespdb.IntegrityMeta{ObjectSize: 8}, nil)
				db.EXPECT().UpdatePieceChecksum(uint64(1), int32(-1), hash.GenerateChecksum([]byte("qrs")), uint64(3)).Return(nil)
				db.EXPECT().GetObjectIntegrity(uint64(1), int32(-1)).Return(&corespdb.IntegrityMeta{ObjectSize: 19}, nil)
				db.EXPECT().UpdateIntegrityChecksum(gomock.Any()).Return(nil)
			}
			task := &gfsptask.GfSpResumableUploadObjectTask{Task: &gfsptask.GfSpTask{}, ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1), PayloadSize: 19, IsUpdating: updating}, StorageParams: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 8}}}
			require.ErrorIs(t, u.HandleResumableUploadObjectTask(context.Background(), task, strings.NewReader("abcdefghijk")), ErrClosedStream)
			<-reported
			resumed := &gfsptask.GfSpResumableUploadObjectTask{Task: &gfsptask.GfSpTask{}, ObjectInfo: task.ObjectInfo, StorageParams: task.StorageParams, Offset: 8, Completed: true, IsAgentUpload: true}
			require.NoError(t, u.validateResumableUploadOffset(resumed))
			require.NoError(t, u.HandleResumableUploadObjectTask(context.Background(), resumed, strings.NewReader("ijklmnopqrs")))
			<-reported
		})
	}
}
