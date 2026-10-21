package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/0xPolygon/polygon-edge/bls"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca-storage-provider/pkg/metrics"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualtypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

const blsLogChildEnv = "MOCA_EXECUTOR_BLS_LOG_TEST_CHILD"

type replicateLogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *replicateLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
func (c *replicateLogCapture) Sync() error { return nil }
func (c *replicateLogCapture) Stop() error { return nil }
func (c *replicateLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// Override only the post-replication read: production first verifies and replaces
// RPC signatures, so preloading a malformed real task cannot reach this branch.
type replicateSignatureTask struct {
	*gfsptask.GfSpReplicatePieceTask
	signatures [][]byte
}

func (t *replicateSignatureTask) GetSecondarySignatures() [][]byte { return t.signatures }

func TestHandleReplicatePieceTask_BlsParseFailureLogRedaction(t *testing.T) {
	if os.Getenv(blsLogChildEnv) != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(executable, "-test.run=^TestHandleReplicatePieceTask_BlsParseFailureLogRedaction$", "-test.v")
		cmd.Env = append(os.Environ(), blsLogChildEnv+"=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	// Isolate global logger state in this subprocess; no parent writer is replaced.
	capture := &replicateLogCapture{}
	log.SetWriter(capture)
	log.SetLevel(log.DebugLevel)
	key, err := bls.GenerateBlsKey()
	require.NoError(t, err)
	signature, err := key.Sign([]byte("executor log regression"), []byte("executor-log-test"))
	require.NoError(t, err)
	valid, err := signature.Marshal()
	require.NoError(t, err)
	cases := []struct {
		name       string
		signatures [][]byte
		badIndex   int
	}{
		{"short first", [][]byte{[]byte("malformed-secondary-signature"), valid}, 0},
		{"invalid point later", [][]byte{valid, bytes.Repeat([]byte{255}, len(valid))}, 1},
		{"nil first", [][]byte{nil, valid}, 0},
		{"empty later", [][]byte{valid, {}}, 1},
		{"valid control", [][]byte{valid, valid}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeLog := len(capture.String())
			e := setup(t)
			ctrl := gomock.NewController(t)
			chain := consensus.NewMockConsensus(ctrl)
			chain.EXPECT().QueryObjectInfo(gomock.Any(), "bucket", "object").Return(&storagetypes.ObjectInfo{}, nil)
			e.baseApp.SetConsensus(chain)
			pieceOp := piecestore.NewMockPieceOp(ctrl)
			pieceOp.EXPECT().SegmentPieceCount(gomock.Any(), gomock.Any()).Return(uint32(0))
			e.baseApp.SetPieceOp(pieceOp)
			client := gfspclient.NewMockGfSpClientAPI(ctrl)
			client.EXPECT().GetGlobalVirtualGroupByGvgID(gomock.Any(), uint32(7)).Return(&virtualtypes.GlobalVirtualGroup{Id: 7}, nil)
			e.baseApp.SetGfSpClient(client)
			task := &replicateSignatureTask{GfSpReplicatePieceTask: &gfsptask.GfSpReplicatePieceTask{
				Task: &gfsptask.GfSpTask{}, GlobalVirtualGroupId: 7,
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(42), BucketName: "bucket", ObjectName: "object", RedundancyType: storagetypes.REDUNDANCY_REPLICA_TYPE},
				StorageParams: &storagetypes.Params{},
			}, signatures: tc.signatures}
			counter := metrics.ExecutorCounter.WithLabelValues(ExecutorSuccessReplicateAllPiece)
			beforeCounter := replicateCounterValue(t, counter)
			if tc.badIndex < 0 {
				expected, err := bls.Signatures{signature, signature}.Aggregate().Marshal()
				require.NoError(t, err)
				client.EXPECT().SealObject(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, msg *storagetypes.MsgSealObject) (string, error) {
					require.Equal(t, expected, msg.SecondarySpBlsAggSignatures)
					require.Equal(t, uint32(7), msg.GlobalVirtualGroupId)
					return "seal-tx", nil
				})
				e.maxListenSealRetry = 1
				chain.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
			}
			e.HandleReplicatePieceTask(context.Background(), task)
			require.NoError(t, task.Error()) // Preserve the existing loop-local err semantics.
			logged := capture.String()[beforeLog:]
			require.Contains(t, logged, "succeed to replicate all pieces")
			var events []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["msg"] == "failed to generate multiple signatures" {
					events = append(events, event)
				}
			}
			if tc.badIndex < 0 {
				require.Empty(t, events)
				require.True(t, task.GetSealed())
				require.Equal(t, beforeCounter+1, replicateCounterValue(t, counter))
				return
			}
			require.False(t, task.GetSealed())
			require.Equal(t, beforeCounter, replicateCounterValue(t, counter))
			require.Len(t, events, 1)
			event := events[0]
			require.Equal(t, "error", event["l"])
			allowed := map[string]bool{"l": true, "t": true, "caller": true, "msg": true, "signature_index": true, "signature_length": true, "object_id": true, "gvg_id": true, "error": true}
			for field := range event {
				require.True(t, allowed[field], "unexpected field %s in target ERROR: %s", field, logged)
			}
			require.Equal(t, float64(tc.badIndex), event["signature_index"])
			require.Equal(t, float64(len(tc.signatures[tc.badIndex])), event["signature_length"])
			require.Equal(t, "42", event["object_id"])
			require.Equal(t, float64(7), event["gvg_id"])
			require.NotEmpty(t, event["error"])
			encoded, err := json.Marshal(event)
			require.NoError(t, err)
			for _, sig := range tc.signatures {
				if len(sig) == 0 {
					continue
				}
				for _, material := range []string{base64.StdEncoding.EncodeToString(sig), hex.EncodeToString(sig)} {
					require.NotContains(t, string(encoded), material)
				}
				for _, value := range event {
					require.NotEqual(t, string(sig), value)
				}
			}
		})
	}
}

func replicateCounterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	metric := &dto.Metric{}
	require.NoError(t, counter.Write(metric))
	return metric.GetCounter().GetValue()
}
