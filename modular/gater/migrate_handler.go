package gater

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"

	permissiontypes "github.com/mocachain/moca/v2/x/permission/types"

	"github.com/mocachain/moca-storage-provider/base/types/gfsperrors"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	modelgateway "github.com/mocachain/moca-storage-provider/model/gateway"
	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca/v2/types/common"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// dest sp receive migrate gvg notify from src sp.
func (g *GateModular) notifyMigrateSwapOutHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err        error
		reqCtx     *RequestContext
		swapOutMsg []byte
	)
	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	migrateSwapOutHeader := r.Header.Get(GnfdMigrateSwapOutMsgHeader)
	if swapOutMsg, err = hex.DecodeString(migrateSwapOutHeader); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to parse migrate swap out header", "error", err)
		err = ErrDecodeMsg
		return
	}
	swapOut := virtualgrouptypes.MsgSwapOut{}
	if err = json.Unmarshal(swapOutMsg, &swapOut); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to unmarshal migrate swap out msg", "error", err)
		err = ErrDecodeMsg
		return
	}
	if err = g.baseApp.GfSpClient().NotifyMigrateSwapOut(reqCtx.Context(), &swapOut); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to notify migrate swap out", "swap_out", swapOut, "error", err)
		err = ErrNotifySwapOutWithDetail("failed to notify migrate swap out, swap_out: " + swapOut.String() + ",error: " + err.Error())
		return
	}
}

func (g *GateModular) checkMigratePieceAuth(reqCtx *RequestContext, migrateGVGHeader string) (bool, error) {
	var (
		err           error
		migrateGVGMsg []byte
	)
	migrateGVGMsg, err = hex.DecodeString(migrateGVGHeader)
	if err != nil {
		log.Errorw("failed to parse migrate gvg header", "migrate_gvg_header", migrateGVGHeader, "error", err)
		return false, ErrDecodeMsg
	}
	migrateGVG := gfsptask.GfSpMigrateGVGTask{}
	err = json.Unmarshal(migrateGVGMsg, &migrateGVG)
	if err != nil {
		log.Errorw("failed to unmarshal migrate gvg msg", "error", err)
		return false, ErrDecodeMsg
	}
	if migrateGVG.GetExpireTime() < time.Now().Unix() {
		log.Errorw("failed to check migrate gvg expire time", "gvg_task", migrateGVG)
		return false, ErrNoPermission
	}
	destSPAddr, err := reqCtx.verifyTaskSignature(migrateGVG.GetSignBytes(), migrateGVG.GetSignature())
	if err != nil {
		log.Errorw("failed to verify task signature", "gvg_task", migrateGVG, "error", err)
		return false, err
	}
	sp, err := g.spCachePool.QuerySPByAddress(destSPAddr.String())
	if err != nil {
		log.Errorw("failed to query sp", "gvg_task", migrateGVG, "dest_sp_addr", destSPAddr.String(), "error", err)
		return false, err
	}
	effect, err := g.baseApp.GfSpClient().VerifyMigrateGVGPermission(reqCtx.Context(), migrateGVG.GetBucketID(), migrateGVG.GetSrcGvg().GetId(), sp.GetId())
	if effect == nil || err != nil {
		log.Errorw("failed to verify migrate gvg permission", "gvg_bucket_id", migrateGVG.GetBucketID(),
			"src_gvg_id", migrateGVG.GetSrcGvg().GetId(), "dest_sp", sp.GetId(), "effect", effect, "error", err)
		return false, err
	}
	if *effect == permissiontypes.EFFECT_ALLOW {
		return true, nil
	}
	return false, nil
}

// checkMigrateBucketQuotaAuth parse migrateBucketMsgHeader to GfSpBucketMigrationInfo and check if request has permission
// only used by preMigrateBucketHandler and postMigrateBucketHandler
func (g *GateModular) checkMigrateBucketQuotaAuth(reqCtx *RequestContext, migrateBucketMsgHeader string) (bool, *gfsptask.GfSpBucketMigrationInfo, error) {
	allow, bucketMigrationInfo, sp, err := g.verifySignatureAndSP(reqCtx, migrateBucketMsgHeader)
	if !allow {
		return allow, bucketMigrationInfo, err
	}

	effect, err := g.baseApp.GfSpClient().VerifyMigrateGVGPermission(reqCtx.Context(), bucketMigrationInfo.GetBucketId(), 0 /*not used*/, sp.GetId())
	if effect == nil {
		if err != nil && strings.Contains(err.Error(), "the bucket is not in migration status") {
			// metadata service support cancel & completion migrate bucket
			return true, bucketMigrationInfo, nil
		}
		log.Errorw("failed to verify migrate bucket permission", "bucket_migration_info", bucketMigrationInfo,
			"dest_sp", sp.GetId(), "effect", effect, "error", err)
		return false, bucketMigrationInfo, err
	}

	return *effect == permissiontypes.EFFECT_ALLOW, bucketMigrationInfo, nil
}

// verifySignatureAndSP parse migrateBucketMsgHeader to GfSpBucketMigrationInfo and check if request has permission
func (g *GateModular) verifySignatureAndSP(reqCtx *RequestContext, migrateBucketMsgHeader string) (bool, *gfsptask.GfSpBucketMigrationInfo, *sptypes.StorageProvider, error) {
	var (
		err              error
		migrateBucketMsg []byte
		sp               *sptypes.StorageProvider
	)
	migrateBucketMsg, err = hex.DecodeString(migrateBucketMsgHeader)
	if err != nil {
		log.Errorw("failed to parse migrate bucket msg header", "migrate_bucket_header", migrateBucketMsg, "error", err)
		return false, &gfsptask.GfSpBucketMigrationInfo{}, sp, ErrDecodeMsg
	}
	bucketMigrationInfo := gfsptask.GfSpBucketMigrationInfo{}
	if err = json.Unmarshal(migrateBucketMsg, &bucketMigrationInfo); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to unmarshal post migrate bucket msg", "error", err)
		err = ErrDecodeMsg
		return false, &bucketMigrationInfo, sp, err
	}
	if bucketMigrationInfo.GetExpireTime() < time.Now().Unix() {
		log.Errorw("failed to check migrate bucket expire time", "bucket_migration_info", bucketMigrationInfo)
		return false, &bucketMigrationInfo, sp, ErrNoPermission
	}
	destSPAddr, err := reqCtx.verifyTaskSignature(bucketMigrationInfo.GetSignBytes(), bucketMigrationInfo.GetSignature())
	if err != nil {
		log.Errorw("failed to verify task signature", "bucket_migration_info", bucketMigrationInfo, "error", err)
		return false, &bucketMigrationInfo, sp, err
	}
	sp, err = g.spCachePool.QuerySPByAddress(destSPAddr.String())
	if err != nil {
		log.Errorw("failed to query sp", "bucket_migration_info", bucketMigrationInfo, "dest_sp_addr", destSPAddr.String(), "error", err)
		return false, &bucketMigrationInfo, sp, err
	}
	return true, &bucketMigrationInfo, sp, nil
}

// migratePieceHandler handles migrate piece request between SPs which is used in SP exiting case.
func (g *GateModular) migratePieceHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err             error
		allowMigrate    bool
		reqCtx          *RequestContext
		migratePieceMsg []byte
		pieceKey        string
		pieceSize       int64
		pieceData       []byte
	)
	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	migratePieceHeader := r.Header.Get(GnfdMigratePieceMsgHeader)
	migratePieceMsg, err = hex.DecodeString(migratePieceHeader)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to parse migrate piece header", "error", err)
		err = ErrDecodeMsg
		return
	}

	migratePiece := gfsptask.GfSpMigratePieceTask{}
	err = json.Unmarshal(migratePieceMsg, &migratePiece)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to unmarshal migrate piece msg", "error", err)
		err = ErrDecodeMsg
		return
	}

	if allowMigrate, err = g.checkMigratePieceAuth(reqCtx, r.Header.Get(GnfdMigrateGVGMsgHeader)); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to check migrate piece auth", "migrate_piece", migratePiece, "error", err)
		return
	}
	if !allowMigrate {
		log.CtxErrorw(reqCtx.Context(), "no permission to migrate piece", "migrate_piece", migratePiece)
		err = ErrNoPermission
		return
	}

	objectInfo := migratePiece.GetObjectInfo()
	if objectInfo == nil {
		log.CtxError(reqCtx.Context(), "failed to get migrate piece object info due to has no object info")
		err = ErrInvalidHeader
		return
	}
	chainObjectInfo, bucketInfo, params, err := g.getObjectChainMeta(reqCtx.Context(), objectInfo.GetObjectName(), objectInfo.GetBucketName())
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to get object on chain meta", "error", err)
		err = ErrInvalidHeader
		return
	}

	redundancyNumber := int32(migratePiece.GetStorageParams().GetRedundantDataChunkNum()+migratePiece.GetStorageParams().GetRedundantParityChunkNum()) - 1
	objectID := migratePiece.GetObjectInfo().Id.Uint64()
	objectVersion := migratePiece.GetObjectInfo().GetVersion()

	segmentIdx := migratePiece.GetSegmentIdx()
	redundancyIdx := migratePiece.GetRedundancyIdx()
	if redundancyIdx < piecestore.PrimarySPRedundancyIndex || redundancyIdx > redundancyNumber {
		err = ErrInvalidRedundancyIndex
		return
	}
	if redundancyIdx == piecestore.PrimarySPRedundancyIndex {
		pieceKey = g.baseApp.PieceOp().SegmentPieceKey(objectID, segmentIdx, objectVersion)
		pieceSize = g.baseApp.PieceOp().SegmentPieceSize(migratePiece.ObjectInfo.GetPayloadSize(),
			segmentIdx, migratePiece.GetStorageParams().GetMaxSegmentSize())
	} else {
		pieceKey = g.baseApp.PieceOp().ECPieceKey(objectID, segmentIdx, uint32(redundancyIdx), objectVersion)
		pieceSize = g.baseApp.PieceOp().ECPieceSize(migratePiece.ObjectInfo.GetPayloadSize(), segmentIdx,
			migratePiece.GetStorageParams().GetMaxSegmentSize(), migratePiece.GetStorageParams().GetRedundantDataChunkNum())
	}

	pieceTask := &gfsptask.GfSpDownloadPieceTask{}
	pieceTask.InitDownloadPieceTask(chainObjectInfo, bucketInfo, params, coretask.DefaultSmallerPriority, false, bucketInfo.Owner,
		uint64(pieceSize), pieceKey, 0, uint64(pieceSize),
		g.baseApp.TaskTimeout(pieceTask, objectInfo.GetPayloadSize()), g.baseApp.TaskMaxRetry(pieceTask))
	pieceData, err = g.baseApp.GfSpClient().GetPiece(reqCtx.Context(), pieceTask)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to download segment piece", "piece_key", pieceKey, "error", err)
		return
	}

	_, err = w.Write(pieceData)
	if err != nil {
		err = ErrReplyData
		log.CtxErrorw(reqCtx.Context(), "failed to reply the migrate data", "error", err)
		return
	}
	log.CtxInfow(reqCtx.Context(), "succeed to migrate one piece", "object_id", objectID, "segment_piece_index",
		segmentIdx, "redundancy_index", redundancyIdx)
}

// checkSecondaryBlsMigrationBucketApproval checks that the requested bls attestation
// matches the on-chain bucket migration this SP is a destination secondary SP of.
// The request itself carries no caller identity, so the sign doc is only signed when
// the chain confirms every claim it makes.
func (g *GateModular) checkSecondaryBlsMigrationBucketApproval(ctx context.Context, signDoc *storagetypes.SecondarySpMigrationBucketSignDoc) error {
	if signDoc.GetChainId() != g.baseApp.ChainID() {
		log.CtxErrorw(ctx, "chain id mismatch", "expected", g.baseApp.ChainID(), "actual", signDoc.GetChainId())
		return ErrValidateMsg
	}
	// a sign doc without a bucket id unmarshals into a nil Uint, every method on it panics
	if signDoc.BucketId.IsNil() {
		log.CtxError(ctx, "sign doc carries no bucket id")
		return ErrValidateMsg
	}
	spID, err := g.getSPID()
	if err != nil {
		return ErrConsensusWithDetail("failed to query sp id, error: " + err.Error())
	}
	dstGVG, err := g.baseApp.Consensus().QueryGlobalVirtualGroup(ctx, signDoc.GetDstGlobalVirtualGroupId())
	if err != nil {
		return ErrConsensusWithDetail("failed to query dst gvg, gvg_id: " +
			fmt.Sprint(signDoc.GetDstGlobalVirtualGroupId()) + ", error: " + err.Error())
	}
	// this SP only attests migrations into a gvg it is a secondary SP of
	if !slices.Contains(dstGVG.GetSecondarySpIds(), spID) {
		log.CtxErrorw(ctx, "sp is not a secondary sp of the dst gvg", "dst_gvg", dstGVG, "sp_id", spID)
		return ErrNoPermission
	}
	// and the dst gvg must belong to the destination primary SP named in the sign doc
	if dstGVG.GetPrimarySpId() != signDoc.GetDstPrimarySpId() {
		log.CtxErrorw(ctx, "dst gvg primary sp mismatch", "dst_gvg", dstGVG,
			"dst_primary_sp_id", signDoc.GetDstPrimarySpId())
		return ErrNoPermission
	}
	// the bucket must be migrating to that destination SP
	effect, err := g.baseApp.GfSpClient().VerifyMigrateGVGPermission(ctx, signDoc.BucketId.Uint64(),
		signDoc.GetSrcGlobalVirtualGroupId(), signDoc.GetDstPrimarySpId())
	if err != nil {
		return ErrConsensusWithDetail("failed to verify migrate gvg permission, bucket_id: " +
			signDoc.BucketId.String() + ", error: " + err.Error())
	}
	if effect == nil || *effect != permissiontypes.EFFECT_ALLOW {
		log.CtxErrorw(ctx, "no permission to attest the bucket migration", "sign_doc", signDoc.String(), "effect", effect)
		return ErrNoPermission
	}
	return nil
}

// checkSwapOutApproval checks that this SP is the successor named in the swap out and
// that the requesting SP is really leaving the family or the gvgs it claims, before the
// approval key signs the message.
func (g *GateModular) checkSwapOutApproval(ctx context.Context, swapOut *virtualgrouptypes.MsgSwapOut) error {
	spID, err := g.getSPID()
	if err != nil {
		return ErrConsensusWithDetail("failed to query sp id, error: " + err.Error())
	}
	if swapOut.GetSuccessorSpId() != spID {
		log.CtxErrorw(ctx, "swap out successor is another sp", "successor_sp_id", swapOut.GetSuccessorSpId(), "sp_id", spID)
		return ErrNoPermission
	}
	// query the chain directly instead of the sp cache pool, the decision depends on
	// the sp status and the cache pool serves entries up to 30 minutes old
	srcSP, err := g.baseApp.Consensus().QuerySP(ctx, swapOut.GetStorageProvider())
	if err != nil {
		return ErrConsensusWithDetail("failed to query the swap out sp, sp: " +
			swapOut.GetStorageProvider() + ", error: " + err.Error())
	}
	if srcSP.GetStatus() != sptypes.STATUS_GRACEFUL_EXITING && srcSP.GetStatus() != sptypes.STATUS_FORCED_EXITING {
		log.CtxErrorw(ctx, "swap out sp is not exiting", "sp", srcSP.GetOperatorAddress(), "status", srcSP.GetStatus())
		return ErrNoPermission
	}
	if familyID := swapOut.GetGlobalVirtualGroupFamilyId(); familyID != virtualgrouptypes.NoSpecifiedFamilyID {
		// swap out as the primary SP of the family
		family, familyErr := g.baseApp.Consensus().QueryVirtualGroupFamily(ctx, familyID)
		if familyErr != nil {
			return ErrConsensusWithDetail("failed to query virtual group family, family_id: " +
				fmt.Sprint(familyID) + ", error: " + familyErr.Error())
		}
		if family.GetPrimarySpId() != srcSP.GetId() {
			log.CtxErrorw(ctx, "swap out family does not belong to the requesting sp", "family", family,
				"sp_id", srcSP.GetId())
			return ErrNoPermission
		}
		return nil
	}
	// swap out as a secondary SP of the listed gvgs
	if len(swapOut.GetGlobalVirtualGroupIds()) == 0 {
		log.CtxError(ctx, "swap out has neither a family nor gvg ids")
		return ErrValidateMsg
	}
	for _, gvgID := range swapOut.GetGlobalVirtualGroupIds() {
		gvg, gvgErr := g.baseApp.Consensus().QueryGlobalVirtualGroup(ctx, gvgID)
		if gvgErr != nil {
			return ErrConsensusWithDetail("failed to query gvg, gvg_id: " + fmt.Sprint(gvgID) +
				", error: " + gvgErr.Error())
		}
		if !slices.Contains(gvg.GetSecondarySpIds(), srcSP.GetId()) {
			log.CtxErrorw(ctx, "swap out gvg does not contain the requesting sp", "gvg", gvg, "sp_id", srcSP.GetId())
			return ErrNoPermission
		}
	}
	return nil
}

// getSecondaryBlsMigrationBucketApprovalHandler handles the bucket migration approval request for secondarySP using bls
func (g *GateModular) getSecondaryBlsMigrationBucketApprovalHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err                        error
		reqCtx                     *RequestContext
		migrationBucketApprovalMsg []byte
	)
	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	migrationBucketApprovalHeader := r.Header.Get(GnfdSecondarySPMigrationBucketMsgHeader)
	migrationBucketApprovalMsg, err = hex.DecodeString(migrationBucketApprovalHeader)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to parse secondary migration bucket approval header", "error", err)
		err = ErrDecodeMsg
		return
	}

	signDoc := &storagetypes.SecondarySpMigrationBucketSignDoc{}
	if err = storagetypes.ModuleCdc.UnmarshalJSON(migrationBucketApprovalMsg, signDoc); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to unmarshal migration bucket approval msg", "error", err)
		err = ErrDecodeMsg
		return
	}
	if err = g.checkSecondaryBlsMigrationBucketApproval(reqCtx.Context(), signDoc); err != nil {
		log.CtxErrorw(reqCtx.Context(), "refuse to sign secondary sp migration bucket approval",
			"sign_doc", signDoc.String(), "error", err)
		return
	}
	signature, err := g.baseApp.GfSpClient().SignSecondarySPMigrationBucket(reqCtx.Context(), signDoc)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to sign secondary sp migration bucket", "error", err)
		err = ErrMigrateApprovalWithDetail("failed to sign secondary sp migration bucket, bucket_id: " + signDoc.BucketId.String() + " ,error: " + err.Error())
		return
	}
	w.Header().Set(GnfdSecondarySPMigrationBucketApprovalHeader, hex.EncodeToString(signature))
	log.CtxInfow(reqCtx.Context(), "succeed to sign secondary sp migration bucket approval", "bucket_id",
		signDoc.BucketId.String())
}

func (g *GateModular) getSwapOutApproval(w http.ResponseWriter, r *http.Request) {
	var (
		err                error
		reqCtx             *RequestContext
		swapOutApprovalMsg []byte
	)
	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	swapOutApprovalHeader := r.Header.Get(GnfdUnsignedApprovalMsgHeader)
	swapOutApprovalMsg, err = hex.DecodeString(swapOutApprovalHeader)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to parse swap out approval header", "error", err)
		err = ErrDecodeMsg
		return
	}

	swapOutApproval := &virtualgrouptypes.MsgSwapOut{}
	if err = virtualgrouptypes.ModuleCdc.UnmarshalJSON(swapOutApprovalMsg, swapOutApproval); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to unmarshal swap out approval msg", "error", err)
		err = ErrDecodeMsg
		return
	}
	swapOutApproval.SuccessorSpApproval = &common.Approval{ExpiredHeight: 100}

	if err = swapOutApproval.ValidateBasic(); err != nil {
		log.Errorw("failed to basic check approval msg", "swap_out_approval", swapOutApproval, "error", err)
		err = ErrValidateMsg
		return
	}

	if err = g.checkSwapOutApproval(reqCtx.Context(), swapOutApproval); err != nil {
		log.CtxErrorw(reqCtx.Context(), "refuse to sign swap out approval",
			"swap_out", swapOutApproval.String(), "error", err)
		return
	}

	log.CtxInfow(reqCtx.Context(), "get swap out approval", "swap_out", swapOutApproval.String())
	signature, err := g.baseApp.GfSpClient().SignSwapOut(reqCtx.Context(), swapOutApproval)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to sign swap out", "error", err)
		err = ErrMigrateApprovalWithDetail("failed to sign swap out, context:" + reqCtx.String() + ", error: " + err.Error())
		return
	}
	swapOutApproval.SuccessorSpApproval.Sig = signature
	bz := storagetypes.ModuleCdc.MustMarshalJSON(swapOutApproval)
	w.Header().Set(GnfdSignedApprovalMsgHeader, hex.EncodeToString(sdktypes.MustSortJSON(bz)))
	log.CtxInfow(reqCtx.Context(), "succeed to sign swap out approval", "swap_out", swapOutApproval.String())
}

// getLatestBucketQuotaHandler handles the query quota request for bucket migrate
func (g *GateModular) getLatestBucketQuotaHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err                 error
		reqCtx              *RequestContext
		bucketID            uint64
		bucketMigrationInfo *gfsptask.GfSpBucketMigrationInfo
		allowMigrate        bool
		bz                  []byte
		quota               *gfsptask.GfSpBucketQuotaInfo
	)

	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	if allowMigrate, bucketMigrationInfo, _, err = g.verifySignatureAndSP(reqCtx, r.Header.Get(GnfdMigrateBucketMsgHeader)); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to check migrate bucket auth", "bucket_migration_info", bucketMigrationInfo, "error", err)
		return
	}
	if !allowMigrate {
		log.CtxErrorw(reqCtx.Context(), "no permission to get latest bucket quota", "bucket_migration_info", bucketMigrationInfo)
		err = ErrNoPermission
		return
	}

	bucketID = bucketMigrationInfo.GetBucketId()
	if quota, err = g.baseApp.GfSpClient().GetLatestBucketReadQuota(reqCtx.Context(), bucketID); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to get bucket read quota", "bucket_id",
			bucketID, "error", err)
		return
	}

	if bz, err = quota.Marshal(); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to marshal", "bucket_id",
			bucketID, "error", err)
		return
	}

	w.Header().Set(GnfdQuotaInfoHeader, hex.EncodeToString(bz))
	w.Header().Set(ContentTypeHeader, ContentTypeXMLHeaderValue)

	log.CtxInfow(reqCtx.Context(), "succeed to get latest bucket quota", "bucket_id",
		bucketID, "quota", quota)
}

// preMigrateBucketHandler handles the prepare request for bucket migrate
func (g *GateModular) preMigrateBucketHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err                 error
		reqCtx              *RequestContext
		bucketID            uint64
		bucketSize          uint64
		bucketMigrationInfo *gfsptask.GfSpBucketMigrationInfo
		allowMigrate        bool
		quota               *gfsptask.GfSpBucketQuotaInfo
		bz                  []byte
	)

	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	if allowMigrate, bucketMigrationInfo, err = g.checkMigrateBucketQuotaAuth(reqCtx, r.Header.Get(GnfdMigrateBucketMsgHeader)); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to check migrate bucket auth", "bucket_migration_info", bucketMigrationInfo, "error", err)
		return
	}
	if !allowMigrate {
		log.CtxErrorw(reqCtx.Context(), "no permission to pre migrate bucket", "bucket_migration_info", bucketMigrationInfo)
		err = ErrNoPermission
		return
	}

	bucketID = bucketMigrationInfo.GetBucketId()
	if quota, err = g.baseApp.GfSpClient().NotifyPreMigrateBucketAndDeductQuota(reqCtx.Context(), bucketID); err != nil || quota == nil {
		log.CtxErrorw(reqCtx.Context(), "failed to pre migrate bucket, the bucket may already notified", "bucket_id",
			bucketID, "error", err)
		// if the bucket has already pre notified ignore the error
		if strings.Contains(err.Error(), "the bucket has already notified") {
			err = nil
		}
		return
	}

	if bz, err = quota.Marshal(); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to marshal", "bucket_id",
			bucketID, "error", err)
		return
	}

	w.Header().Set(GnfdQuotaInfoHeader, hex.EncodeToString(bz))
	w.Header().Set(ContentTypeHeader, ContentTypeXMLHeaderValue)

	log.CtxInfow(reqCtx.Context(), "succeed to pre bucket migrate and deduct quota", "bucket_id",
		bucketID, "quota", quota, "bucket_quota_size", bucketSize)
}

// postMigrateBucketHandler notifying the source sp about the completion of migration bucket
func (g *GateModular) postMigrateBucketHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err      error
		reqCtx   *RequestContext
		bucketID uint64

		bucketMigrationInfo *gfsptask.GfSpBucketMigrationInfo
		allowMigrate        bool
		latestQuota         *gfsptask.GfSpBucketQuotaInfo
	)

	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	if allowMigrate, bucketMigrationInfo, err = g.checkMigrateBucketQuotaAuth(reqCtx, r.Header.Get(GnfdMigrateBucketMsgHeader)); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to check migrate bucket auth", "bucket_migration_info", bucketMigrationInfo, "error", err)
		return
	}
	if !allowMigrate {
		log.CtxErrorw(reqCtx.Context(), "no permission to post migrate bucket", "bucket_migration_info", bucketMigrationInfo)
		err = ErrNoPermission
		return
	}

	bucketID = bucketMigrationInfo.GetBucketId()
	if latestQuota, err = g.baseApp.GfSpClient().NotifyPostMigrateBucketAndRecoupQuota(reqCtx.Context(), bucketMigrationInfo); err != nil {
		log.CtxErrorw(reqCtx.Context(), "post migrate bucket error, the bucket may already notified", "bucket_id",
			bucketID, "error", err)
		return
	}

	bz, err := latestQuota.Marshal()
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to marshal", "bucket_id",
			bucketID, "error", err)
		return
	}
	w.Header().Set(GnfdQuotaInfoHeader, hex.EncodeToString(bz))
	w.Header().Set(ContentTypeHeader, ContentTypeXMLHeaderValue)

	log.CtxInfow(reqCtx.Context(), "succeed to post bucket migrate", "bucket_id",
		bucketID, "latest_quota", latestQuota, "bucket_migration_info", bucketMigrationInfo)
}

// sufficientQuotaForBucketMigrationHandler check if the source SP node has sufficient quota for bucket migration at approval phase
func (g *GateModular) sufficientQuotaForBucketMigrationHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err                 error
		reqCtx              *RequestContext
		bucketID            uint64
		bucketSize          uint64
		bucketMigrationInfo *gfsptask.GfSpBucketMigrationInfo
		allowMigrate        bool
	)

	defer func() {
		reqCtx.Cancel()
		if err != nil {
			reqCtx.SetError(err)
			reqCtx.SetHTTPCode(int(gfsperrors.MakeGfSpError(err).GetHttpStatusCode()))
			modelgateway.MakeErrorResponse(w, gfsperrors.MakeGfSpError(err))
		} else {
			reqCtx.SetHTTPCode(http.StatusOK)
		}
		log.CtxDebugw(reqCtx.Context(), reqCtx.String())
	}()

	reqCtx, _ = NewRequestContext(r, g)
	if allowMigrate, bucketMigrationInfo, _, err = g.verifySignatureAndSP(reqCtx, r.Header.Get(GnfdMigrateBucketMsgHeader)); err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to check migrate bucket auth", "bucket_migration_info", bucketMigrationInfo, "error", err)
		return
	}
	if !allowMigrate {
		log.CtxErrorw(reqCtx.Context(), "no permission to check source sp has enough quota", "bucket_migration_info", bucketMigrationInfo)
		err = ErrNoPermission
		return
	}

	bucketID = bucketMigrationInfo.GetBucketId()
	quota, err := g.baseApp.GfSpClient().GetLatestBucketReadQuota(reqCtx.Context(), bucketID)
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to get bucket read quota", "bucket_id",
			bucketID, "error", err)
		return
	}

	// get bucket quota and check
	bucketSize, err = g.getBucketTotalSize(reqCtx.Context(), bucketID)
	if err != nil {
		return
	}

	// empty bucket will approval
	if quota.FreeQuotaSize+quota.ChargedQuotaSize-quota.ReadConsumedSize+quota.MonthlyFreeQuotaSize > bucketSize || bucketSize == 0 {
		quota.AllowMigrate = true
	} else {
		quota.AllowMigrate = false
	}

	bz, err := quota.Marshal()
	if err != nil {
		log.CtxErrorw(reqCtx.Context(), "failed to marshal", "bucket_id",
			bucketID, "error", err)
		return
	}

	w.Header().Set(GnfdQuotaInfoHeader, hex.EncodeToString(bz))
	w.Header().Set(ContentTypeHeader, ContentTypeXMLHeaderValue)
	log.CtxInfow(reqCtx.Context(), "succeed to check bucket migrate quota", "bucket_id", bucketID,
		"quota", quota, "bucket_size", bucketSize)
}
