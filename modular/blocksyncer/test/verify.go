package test

import (
	"encoding/json"
	"errors"
	"fmt"
	golog "log"
	"math/big"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forbole/juno/v4/common"
	"github.com/forbole/juno/v4/models"
	"github.com/shopspring/decimal"
	"github.com/spaolacci/murmur3"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	bucketmodules "github.com/mocachain/moca-storage-provider/modular/blocksyncer/modules/bucket"
	"github.com/mocachain/moca-storage-provider/store/bsdb"
	permissiontypes "github.com/mocachain/moca/v2/x/permission/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

const (
	// maxGroupMemberExpirationUnix is storage types MaxTimeStamp (9999-12-31T23:59:59Z) as Unix seconds.
	maxGroupMemberExpirationUnix = int64(253402300799)
	expirationEndOfYear2023Unix  = int64(1704067199)
	expirationEndOfYear3023Unix  = int64(33260975999)
)

var verifyFuncs = []func(t *testing.T, db *gorm.DB) error{
	verify1, verify2, verify3, verify4, verify5, verify6, verify7, verify8, verify9, verify10,
	verify11, verify12, verify13, verify14, verify15, verify16, verify17, verify18, verify19, verify20,
	verify21, verify22, verify23, verify24, verify25, verify26, verify27, verify28, verify29, verify30,
	verify31, verify32, verify33, verify34, verify35, verify36, verify37, verify38, verify39, verify40,
	verify41, verify42, verify43, verify44, verify45, verify46, verify47, verify48, verify49, verify50,
	verify51, verify52, verify53, verify54, verify55, verify56, verify57, verify58, verify59, verify60,
	verify61, verify62, verify63, verify64, verify65, verify66, verify67, verify68, verify69, verify70,
}

func envIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultValue
}

func retryVerify(description string, attempts int, interval time.Duration, fn func() error) error {
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			golog.Printf("%s attempt %d/%d failed: %v", description, attempt, attempts, err)
		}

		if attempt < attempts {
			time.Sleep(interval)
		}
	}

	return fmt.Errorf("%s: %w", description, lastErr)
}

// Verify checks what the blocksyncer wrote to the database behind dsn against the test cases.
func Verify(t *testing.T, dsn string) error {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		panic("failed to connect to the database")
	}

	retryTimeout := time.Duration(envIntOrDefault("BLOCKSYNCER_TEST_VERIFY_TIMEOUT_SECONDS", 120)) * time.Second
	retryInterval := time.Duration(envIntOrDefault("BLOCKSYNCER_TEST_VERIFY_INTERVAL_MS", 500)) * time.Millisecond
	retryAttempts := int(retryTimeout / retryInterval)
	if retryAttempts < 1 {
		retryAttempts = 1
	}

	for i := 1; i <= LatestHeight; i++ {
		err = retryVerify(fmt.Sprintf("wait for epoch height %d", i), retryAttempts, retryInterval, func() error {
			var epoch bsdb.Epoch
			if err := db.Table("epoch").First(&epoch).Error; err != nil {
				return err
			}
			if epoch.BlockHeight < int64(i) {
				return fmt.Errorf("current epoch height %d", epoch.BlockHeight)
			}
			return nil
		})
		if err != nil {
			return err
		}

		f := verifyFuncs[i-1]
		if err := retryVerify(fmt.Sprintf("verify case %d", i), retryAttempts, retryInterval, func() error {
			return f(t, db)
		}); err != nil {
			return err
		}

		golog.Printf("%d case pass", i)

		// height increase
		if i != LatestHeight {
			StatusRes = fmt.Sprintf("{\"sync_info\":{\"latest_block_height\":\"%d\"}}", i+1)
		}
	}

	// verify height
	var epoch bsdb.Epoch
	db.Table("epoch").First(&epoch)
	if epoch.BlockHeight != int64(LatestHeight) {
		return errors.New("height error")
	}

	var count int64
	if err := retryVerify("wait for block_result rows", retryAttempts, retryInterval, func() error {
		if err := db.Table("block_result").Count(&count).Error; err != nil {
			return err
		}
		if count <= 0 {
			return errors.New("block result count error")
		}
		return nil
	}); err != nil {
		return err
	}

	return nil
}

const (
	tagsOwner      = "0x2412c55D1b613C87E37aF2172b05343551D19e98"
	tagsKey12      = `{"tags": [{"key": "key1", "value": "value1"}, {"key": "key2", "value": "value2"}]}`
	tagsKey12Tag04 = `{"tags": [{"key": "key1", "value": "value1"}, {"key": "key2", "value": "value2"}, {"key": "tag04", "value": "tag04_value"}]}`
)

// getRow loads the first row of table matching query into dest and fails when there is none.
func getRow(db *gorm.DB, table string, dest interface{}, query string, args ...interface{}) error {
	if err := db.Table(table).Where(query, args...).Take(dest).Error; err != nil {
		return fmt.Errorf("%s where %s %v: %w", table, query, args, err)
	}
	return nil
}

func eq[T comparable](field string, got, want T) error {
	if got != want {
		return fmt.Errorf("%s: got %v, want %v", field, got, want)
	}
	return nil
}

func eqBig(field string, got *common.Big, want string) error {
	if got == nil {
		return eq(field, "<nil>", want)
	}
	return eq(field, got.Raw().String(), want)
}

// eqJSON compares two JSON documents, reading a NULL column as null.
func eqJSON(field string, got []byte, want string) error {
	if len(got) == 0 {
		got = []byte("null")
	}
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		return fmt.Errorf("%s: got %q: %w", field, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		return fmt.Errorf("%s: want %q: %w", field, want, err)
	}
	if !reflect.DeepEqual(g, w) {
		return fmt.Errorf("%s: got %s, want %s", field, got, want)
	}
	return nil
}

func idHash(id int64) common.Hash {
	return common.BigToHash(big.NewInt(id))
}

func actionBits(actions ...permissiontypes.ActionType) int {
	bits := 0
	for _, action := range actions {
		bits |= 1 << bsdb.ActionTypeMap[action]
	}
	return bits
}

func verifyGroup(db *gorm.DB, id int64, name, owner, tags string) error {
	var g models.Group
	if err := getRow(db, bsdb.GroupTableName, &g, "group_id = ? and account_id = ?", idHash(id), common.HexToAddress("0")); err != nil {
		return err
	}
	return errors.Join(
		eq("group_name", g.GroupName, name),
		eq("owner", g.Owner, common.HexToAddress(owner)),
		eq("source_type", g.SourceType, "SOURCE_TYPE_ORIGIN"),
		eq("removed", g.Removed, false),
		eqJSON("tags", g.Tags, tags),
	)
}

func verifyTaggedBucket(db *gorm.DB, name string, id int64, tags string) error {
	var b models.Bucket
	if err := getRow(db, bsdb.BucketTableName, &b, "bucket_name = ?", name); err != nil {
		return err
	}
	owner := common.HexToAddress(tagsOwner)
	return errors.Join(
		eq("bucket_id", b.BucketID, idHash(id)),
		eq("owner", b.Owner, owner),
		eq("payment_address", b.PaymentAddress, owner),
		eq("global_virtual_group_family_id", b.GlobalVirtualGroupFamilyId, 1),
		eq("charged_read_quota", b.ChargedReadQuota, 100),
		eq("visibility", b.Visibility, "VISIBILITY_TYPE_PRIVATE"),
		eq("status", b.Status, "BUCKET_STATUS_CREATED"),
		eqJSON("tags", b.Tags, tags),
	)
}

func verifyTaggedObject(db *gorm.DB, name string, id, bucketID int64, tags string) error {
	var o models.Object
	if err := getRow(db, GetObjectsTableName("ot005test-bucket"), &o, "bucket_name=? and object_name = ?", "ot005test-bucket", name); err != nil {
		return err
	}
	owner := common.HexToAddress(tagsOwner)
	return errors.Join(
		eq("object_id", o.ObjectID, idHash(id)),
		eq("bucket_id", o.BucketID, idHash(bucketID)),
		eq("creator", o.Creator, owner),
		eq("owner", o.Owner, owner),
		eq("payload_size", o.PayloadSize, 1000),
		eq("visibility", o.Visibility, "VISIBILITY_TYPE_PRIVATE"),
		eq("content_type", o.ContentType, "text/plain"),
		eq("status", o.Status, "OBJECT_STATUS_CREATED"),
		eqJSON("tags", o.Tags, tags),
	)
}

func verifyObjectsRemoved(db *gorm.DB, ids ...int64) error {
	for _, id := range ids {
		var o models.Object
		if err := getRow(db, GetObjectsTableName("cxz"), &o, "object_id = ?", idHash(id)); err != nil {
			return err
		}
		if !o.Removed {
			return fmt.Errorf("object %d is not removed", id)
		}
	}
	return nil
}

func verify1(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "full-node-v1-acc0000000072-buc0000000000").Find(&bucket).Error; err != nil {
		return err
	}
	golog.Println(bucket)
	if bucket.Status != "BUCKET_STATUS_CREATED" {
		return errors.New("bucket status error")
	}
	return nil
}

func verify2(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "full-node-v1-acc0000000072-buc0000000000").Find(&bucket).Error; err != nil {
		return err
	}
	if bucket.Visibility != "VISIBILITY_TYPE_PUBLIC_READ" {
		return errors.New("bucket Visibility is error")
	}
	return nil
}

func verify3(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "full-node-v1-acc0000000072-buc0000000000").Find(&bucket).Error; err != nil {
		return err
	}
	if !bucket.Removed {
		return errors.New("bucket is not remove")
	}
	return nil
}

func verify4(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "cxz").Find(&bucket).Error; err != nil {
		return err
	}
	if bucket.DeleteAt != int64(4) {
		return fmt.Errorf("bucket is not discontinue delete at:%v", bucket.DeleteAt)
	}
	return nil
}

func verify5(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "cxz").Find(&bucket).Error; err != nil {
		return err
	}
	if bucket.GlobalVirtualGroupFamilyId != 9 {
		return errors.New("bucket GlobalVirtualGroupFamilyId error")
	}
	return nil
}

func verify6(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table(bsdb.BucketTableName).Where("bucket_name = ?", "cxz").Find(&bucket).Error; err != nil {
		return err
	}
	if bucket.GlobalVirtualGroupFamilyId != 10 {
		return errors.New("bucket GlobalVirtualGroupFamilyId error")
	}
	return nil
}

func verify7(t *testing.T, db *gorm.DB) error {
	return verifyGroup(db, 2, "n3susb0", "0x6a45dE47a2Cd53084b4793fca7C1e706B9f54ED1", "null")
}

func verify8(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table(bsdb.GroupTableName).Where("group_id = ? and account_id = ?", common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), common.HexToAddress("0x92b7976702C064C7e2e791854497Ec73C853CEB5")).Find(&g).Error; err != nil {
		return err
	}
	if g.ExpirationTime != maxGroupMemberExpirationUnix {
		return errors.New("member ExpirationTime error")
	}
	return nil
}

func verify9(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table(bsdb.GroupTableName).Where("group_id = ? and account_id = ?", common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), common.HexToAddress("0x4d57d300AfaF9f407e26552965ce355786206cF4")).Find(&g).Error; err != nil {
		return err
	}
	if g.ExpirationTime != expirationEndOfYear2023Unix {
		return fmt.Errorf("member ExpirationTime error ExpirationTime:%d account id:%v", g.ExpirationTime, g.AccountID)
	}
	return nil
}

func verify10(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table(bsdb.GroupTableName).Where("group_id = ? and account_id = ?", common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"), common.HexToAddress("0x0000000000000000000000000000000000000000")).Find(&g).Error; err != nil {
		return err
	}
	if !g.Removed {
		return errors.New("group is not removed")
	}
	return nil
}

func verify11(t *testing.T, db *gorm.DB) error {
	var o models.Object
	if err := getRow(db, GetObjectsTableName("cxz"), &o, "object_id = ?", idHash(1201)); err != nil {
		return err
	}
	var m bsdb.ObjectIDMap
	if err := getRow(db, bsdb.ObjectIDMapTableName, &m, "object_id = ?", idHash(1201)); err != nil {
		return err
	}
	creator := common.HexToAddress("0xC56259Acb9c37F96a4476875d486D478a32Dbc26")
	return errors.Join(
		eq("bucket_id", o.BucketID, idHash(649851)),
		eq("bucket_name", o.BucketName, "cxz"),
		eq("object_name", o.ObjectName, "full-node-acc0000001201-buc0000000000-obj0000000000"),
		eq("creator", o.Creator, creator),
		eq("owner", o.Owner, creator),
		eq("payload_size", o.PayloadSize, 1024),
		eq("visibility", o.Visibility, "VISIBILITY_TYPE_INHERIT"),
		eq("content_type", o.ContentType, "application/octet-stream"),
		eq("status", o.Status, "OBJECT_STATUS_CREATED"),
		eq("redundancy_type", o.RedundancyType, "REDUNDANCY_EC_TYPE"),
		eq("source_type", o.SourceType, "SOURCE_TYPE_ORIGIN"),
		eq("checksums", len(o.CheckSums), 7),
		eq("create_time", o.CreateTime, 1692577044),
		eq("object_id_map bucket_name", m.BucketName, "cxz"),
	)
}

func verify12(t *testing.T, db *gorm.DB) error {
	table := GetObjectsTableName("cxz")
	var o models.Object
	if err := db.Table(table).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&o).Error; err != nil {
		return err
	}
	if o.Status != "OBJECT_STATUS_SEALED" {
		return errors.New("object status error")
	}
	var b models.Bucket
	if err := db.Table("buckets").Where("bucket_name =?", "cxz").Find(&b).Error; err != nil {
		return err
	}
	if !b.StorageSize.Equal(decimal.NewFromInt(int64(o.PayloadSize))) {
		return fmt.Errorf("StorageSize: %s error", b.StorageSize.String())
	}
	if !b.ChargeSize.Equal(decimal.NewFromInt(128000)) {
		return fmt.Errorf("ChargeSize: %s error", b.ChargeSize.String())
	}
	return nil
}

func verify13(t *testing.T, db *gorm.DB) error {
	table := GetObjectsTableName("cxz")
	var o models.Object
	if err := db.Table(table).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&o).Error; err != nil {
		return err
	}
	if !o.Removed {
		return errors.New("object is not removed")
	}
	return nil
}

func verify14(t *testing.T, db *gorm.DB) error {
	var sr models.StreamRecord
	if err := getRow(db, (&models.StreamRecord{}).TableName(), &sr, "account = ?", common.HexToAddress("0x51D2c7dFa2a58C73853c2657Dc505e7430879B1b")); err != nil {
		return err
	}
	return errors.Join(
		eq("crud_timestamp", sr.CrudTimestamp, 1692576365),
		eq("settle_timestamp", sr.SettleTimestamp, 0),
		eq("status", sr.Status, "STREAM_ACCOUNT_STATUS_ACTIVE"),
		eqBig("netflow_rate", sr.NetflowRate, "0"),
		eqBig("frozen_netflow_rate", sr.FrozenNetflowRate, "0"),
		eqBig("static_balance", sr.StaticBalance, "0"),
		eqBig("buffer_balance", sr.BufferBalance, "0"),
		eqBig("lock_balance", sr.LockBalance, "2517955200"),
	)
}

func verify15(t *testing.T, db *gorm.DB) error {
	var p models.PaymentAccount
	if err := getRow(db, (&models.PaymentAccount{}).TableName(), &p, "addr = ?", common.HexToAddress("0x68bd245Df652435321989b999F9F70Cd31281b66")); err != nil {
		return err
	}
	return errors.Join(
		eq("owner", p.Owner, common.HexToAddress("0x84A0d38d64498414B14cD979159D57557345Cd8B")),
		eq("refundable", p.Refundable, false),
	)
}

func verify16(t *testing.T, db *gorm.DB) error {
	var lvg models.LocalVirtualGroup
	if err := db.Table((&models.LocalVirtualGroup{}).TableName()).Where("local_virtual_group_id = ?", 1).Find(&lvg).Error; err != nil {
		return err
	}
	if lvg.StoredSize != 1024 {
		return fmt.Errorf("StoredSize error, not updated to 1024, size is %v", lvg.StoredSize)
	}
	return nil
}

func verify17(t *testing.T, db *gorm.DB) error {
	var lvg models.LocalVirtualGroup
	if err := db.Table((&models.LocalVirtualGroup{}).TableName()).Where("local_virtual_group_id = ?", 1).Find(&lvg).Error; err != nil {
		return err
	}
	if lvg.StoredSize != 0 || lvg.GlobalVirtualGroupId != 3 {
		return fmt.Errorf("StoredSize not updated to 0, or gvgId not update to 3, StoredSize is %v and gvgId is %v", lvg.StoredSize, lvg.GlobalVirtualGroupId)
	}
	return nil
}

func verify18(t *testing.T, db *gorm.DB) error {
	var lvg models.LocalVirtualGroup
	if err := db.Table((&models.LocalVirtualGroup{}).TableName()).Where("local_virtual_group_id = ?", 1).Find(&lvg).Error; err != nil {
		return err
	}
	if !lvg.Removed {
		return fmt.Errorf("lvg id %v not removed", lvg.LocalVirtualGroupId)
	}

	return nil
}

func verify19(t *testing.T, db *gorm.DB) error {
	var gvg bsdb.GlobalVirtualGroup
	if err := db.Table((&bsdb.GlobalVirtualGroup{}).TableName()).Where("global_virtual_group_id = ?", 1).Find(&gvg).Error; err != nil {
		return err
	}
	if gvg.StoredSize != 1223 || gvg.TotalDeposit.Raw().String() != "140737488355328000" {
		return fmt.Errorf("gvg create error StoredSize: %v, TotalDeposit:%v", gvg.StoredSize, gvg.TotalDeposit.Raw().String())
	}
	return nil
}

func verify20(t *testing.T, db *gorm.DB) error {
	var gvg bsdb.GlobalVirtualGroup
	if err := db.Table((&bsdb.GlobalVirtualGroup{}).TableName()).Where("global_virtual_group_id = ?", 1).Find(&gvg).Error; err != nil {
		return err
	}
	if gvg.StoredSize != 0 || gvg.TotalDeposit.Raw().String() != "0" || gvg.PrimarySpId != 2 {
		return fmt.Errorf("gvg update error StoredSize: %v, TotalDeposit: %v, PrimarySpId: %v", gvg.StoredSize, gvg.TotalDeposit.Raw().String(), gvg.PrimarySpId)
	}
	return nil
}

func verify21(t *testing.T, db *gorm.DB) error {
	var gvg bsdb.GlobalVirtualGroup
	if err := db.Table((&bsdb.GlobalVirtualGroup{}).TableName()).Where("global_virtual_group_id = ?", 1).Find(&gvg).Error; err != nil {
		return err
	}
	if !gvg.Removed {
		return fmt.Errorf("gvg id %v not removed", gvg.GlobalVirtualGroupId)
	}
	return nil
}

func verify22(t *testing.T, db *gorm.DB) error {
	var gvgf bsdb.GlobalVirtualGroupFamily
	if err := db.Table((&bsdb.GlobalVirtualGroupFamily{}).TableName()).Where("global_virtual_group_family_id = ?", 1).Find(&gvgf).Error; err != nil {
		return err
	}
	if gvgf.PrimarySpId != 3 {
		return fmt.Errorf("create gvgf with PrimarySpId error, expect 3, get %v", gvgf.PrimarySpId)
	}
	return nil
}

func verify23(t *testing.T, db *gorm.DB) error {
	var gvgf bsdb.GlobalVirtualGroupFamily
	if err := db.Table((&bsdb.GlobalVirtualGroupFamily{}).TableName()).Where("global_virtual_group_family_id = ?", 1).Find(&gvgf).Error; err != nil {
		return err
	}
	if gvgf.PrimarySpId != 5 {
		return fmt.Errorf("update gvgf with PrimarySpId error, expect 5, get %v", gvgf.PrimarySpId)
	}
	return nil
}

func verify24(t *testing.T, db *gorm.DB) error {
	var gvgf bsdb.GlobalVirtualGroupFamily
	if err := db.Table((&bsdb.GlobalVirtualGroupFamily{}).TableName()).Where("global_virtual_group_family_id = ?", 1).Find(&gvgf).Error; err != nil {
		return err
	}
	if !gvgf.Removed {
		return fmt.Errorf("gvgf id %v is not removed", gvgf.GlobalVirtualGroupFamilyId)
	}
	return nil
}

func verify25(t *testing.T, db *gorm.DB) error {
	var sp models.StorageProvider
	if err := getRow(db, (&models.StorageProvider{}).TableName(), &sp, "sp_id = ?", 14); err != nil {
		return err
	}
	return errors.Join(
		eq("operator_address", sp.OperatorAddress, common.HexToAddress("0xE127Da5a0138d1953363e6E0bF05d971a7c03481")),
		eq("funding_address", sp.FundingAddress, common.HexToAddress("0xaFB4224211583b00C24F854B3ff74c34190971b9")),
		eq("seal_address", sp.SealAddress, common.HexToAddress("0x573eB6Ad0a2334A7B64726d4f2E7d84EEd56E50d")),
		eq("approval_address", sp.ApprovalAddress, common.HexToAddress("0x58e97D3Edd09FA541D06f34f2783F325870Acaed")),
		eq("gc_address", sp.GcAddress, common.HexToAddress("0x3E467Acb8Fd962BC0C89409b19f61D209852ba30")),
		eqBig("total_deposit", sp.TotalDeposit, "1000000000000000000000"),
		eq("status", sp.Status, "STATUS_IN_MAINTENANCE"),
		eq("endpoint", sp.Endpoint, "https://gf-devnet-sp-aa-bk.dev.nodereal.cc"),
		eq("moniker", sp.Moniker, "QATest"),
		eq("bls_key", sp.BlsKey, "a33e608a5a77cbefa0f33e022d0d47226f389a2ecd3072ea2930f3c64370f3cc84e2af4800652a4dab29fa4802318081"),
	)
}

func verify26(t *testing.T, db *gorm.DB) error {
	var sp models.StorageProvider
	if err := db.Table((&models.StorageProvider{}).TableName()).Where("sp_id = ?", 14).Find(&sp).Error; err != nil {
		return err
	}
	if sp.Endpoint != "http://spxrmfl.moca.io" || sp.BlsKey != "b689357b256f8aabaf02fceb56a9a61c59b2d9b3cc78d4413fefd1e3bd902c90dcc7b346deb672d164c9ff832a8ee1d9" {
		return fmt.Errorf("sp update failed endpoint: %s, blskey:%s", sp.Endpoint, sp.BlsKey)
	}
	return nil
}

func verify27(t *testing.T, db *gorm.DB) error {
	var sp models.StorageProvider
	if err := db.Table((&models.StorageProvider{}).TableName()).Where("sp_id = ?", 14).Find(&sp).Error; err != nil {
		return err
	}
	if !sp.Removed {
		return errors.New("sp is not removed")
	}
	return nil
}

func verify28(t *testing.T, db *gorm.DB) error {
	var permission models.Permission
	if err := getRow(db, (&models.Permission{}).TableName(), &permission, "policy_id = ?", idHash(2)); err != nil {
		return err
	}
	var statements []models.Statements
	if err := db.Table((&models.Statements{}).TableName()).Where("policy_id = ?", idHash(2)).Order("id").Find(&statements).Error; err != nil {
		return err
	}
	if len(statements) != 2 {
		return fmt.Errorf("statements: got %d, want 2", len(statements))
	}
	return errors.Join(
		eq("principal_type", permission.PrincipalType, int32(permissiontypes.PRINCIPAL_TYPE_GNFD_GROUP)),
		eq("principal_value", permission.PrincipalValue, "3"),
		eq("resource_type", permission.ResourceType, "RESOURCE_TYPE_BUCKET"),
		eq("resource_id", permission.ResourceID, idHash(2)),
		eq("expiration_time", permission.ExpirationTime, 0),
		eq("statement 0 effect", statements[0].Effect, "EFFECT_ALLOW"),
		eq("statement 0 actions", statements[0].ActionValue, actionBits(permissiontypes.ACTION_UPDATE_BUCKET_INFO, permissiontypes.ACTION_DELETE_BUCKET, permissiontypes.ACTION_CREATE_OBJECT)),
		eq("statement 0 resources", strings.Join(statements[0].Resources, ","), ""),
		eq("statement 1 effect", statements[1].Effect, "EFFECT_ALLOW"),
		eq("statement 1 actions", statements[1].ActionValue, actionBits(permissiontypes.ACTION_GET_OBJECT, permissiontypes.ACTION_DELETE_OBJECT)),
		eq("statement 1 resources", strings.Join(statements[1].Resources, ","), "grn:o::goatw/.*"),
	)
}

func verify29(t *testing.T, db *gorm.DB) error {
	var permission models.Permission
	if err := db.Table((&models.Permission{}).TableName()).Where("policy_id = ?", common.BigToHash(big.NewInt(2))).Find(&permission).Error; err != nil {
		return err
	}
	if !permission.Removed {
		return errors.New("permission is not removed")
	}
	return nil
}

func verify30(t *testing.T, db *gorm.DB) error {
	var object models.Object
	if err := db.Table(GetObjectsTableName("cxz")).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&object).Error; err != nil {
		return err
	}
	if !object.Removed {
		return errors.New("object is not removed")
	}
	return nil
}

func verify31(t *testing.T, db *gorm.DB) error {
	var object models.Object
	if err := db.Table(GetObjectsTableName("cxz")).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&object).Error; err != nil {
		return err
	}
	if object.Status != storagetypes.OBJECT_STATUS_DISCONTINUED.String() {
		return errors.New("object status error")
	}
	return nil
}

func verify32(t *testing.T, db *gorm.DB) error {
	var o models.Object
	if err := getRow(db, GetObjectsTableName("cxz"), &o, "object_id = ?", idHash(1203)); err != nil {
		return err
	}
	creator := common.HexToAddress("0xC56259Acb9c37F96a4476875d486D478a32Dbc26")
	return errors.Join(
		eq("bucket_id", o.BucketID, idHash(649851)),
		eq("bucket_name", o.BucketName, "cxz"),
		eq("object_name", o.ObjectName, "full-node-acc0000001201-b0"),
		eq("creator", o.Creator, creator),
		eq("owner", o.Owner, creator),
		eq("payload_size", o.PayloadSize, 1024),
		eq("visibility", o.Visibility, "VISIBILITY_TYPE_INHERIT"),
		eq("status", o.Status, "OBJECT_STATUS_CREATED"),
		eq("source_type", o.SourceType, "SOURCE_TYPE_ORIGIN"),
	)
}

func verify33(t *testing.T, db *gorm.DB) error {
	var object models.Object
	if err := db.Table(GetObjectsTableName("cxz")).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&object).Error; err != nil {
		return err
	}
	if !object.Removed || object.Operator.String() != "0x6fD578b6fd9635cB7a7dDF53B13DbB0c873aEFCD" {
		return errors.New("object is not removed")
	}
	return nil
}

func verify34(t *testing.T, db *gorm.DB) error {
	var object models.Object
	if err := db.Table(GetObjectsTableName("cxz")).Where("object_id = ?", common.BigToHash(big.NewInt(1201))).Find(&object).Error; err != nil {
		return err
	}
	if object.Visibility != "VISIBILITY_TYPE_INHERIT" {
		return errors.New("object Visibility error")
	}
	return nil
}

func verify35(t *testing.T, db *gorm.DB) error {
	var o models.Object
	if err := getRow(db, GetObjectsTableName("cxz"), &o, "object_id = ?", idHash(1204)); err != nil {
		return err
	}
	return errors.Join(
		eq("bucket_name", o.BucketName, "cxz"),
		eq("object_name", o.ObjectName, "full-node-acc00000012ssss01-b0/"),
		eq("operator", o.Operator, common.HexToAddress("0xC4a7F70B823D35c52bB89205475868E767e64bA0")),
		eq("owner", o.Owner, common.HexToAddress("0xC56259Acb9c37F96a4476875d486D478a32Dbc26")),
		eq("payload_size", o.PayloadSize, 1024),
		eq("status", o.Status, "OBJECT_STATUS_CREATED"),
	)
}

func verify36(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventCompleteMigrationBucket
	if err := getRow(db, bsdb.EventCompleteMigrationTableName, &e, "bucket_id = ?", idHash(4)); err != nil {
		return err
	}
	return errors.Join(
		eq("bucket_name", e.BucketName, "onkz"),
		eq("operator", e.Operator, common.HexToAddress("0xe7194d50da3412972236Fb90dde4431698423457")),
		eq("global_virtual_group_family_id", e.GlobalVirtualGroupFamilyId, 8),
		eq("src_primary_sp_id", e.SrcPrimarySpId, 1),
	)
}

func verify37(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventMigrationBucket
	if err := getRow(db, bsdb.EventMigrationTableName, &e, "bucket_id = ?", idHash(4)); err != nil {
		return err
	}
	return errors.Join(
		eq("bucket_name", e.BucketName, "onkz"),
		eq("operator", e.Operator, common.HexToAddress("0xD05E628eBB81A2516DCe142702e28E765d463BB5")),
		eq("dst_primary_sp_id", e.DstPrimarySpId, 8),
	)
}

func verify38(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventCancelMigrationBucket
	if err := getRow(db, bsdb.EventCancelMigrationTableName, &e, "bucket_id = ?", idHash(4)); err != nil {
		return err
	}
	return errors.Join(
		eq("bucket_name", e.BucketName, "onkz"),
		eq("operator", e.Operator, common.HexToAddress("0xD05E628eBB81A2516DCe142702e28E765d463BB5")),
	)
}

func verify39(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventStorageProviderExit
	if err := getRow(db, bsdb.EventStorageProviderExitTableName, &e, "storage_provider_id = ?", 9); err != nil {
		return err
	}
	return eq("operator_address", e.OperatorAddress, common.HexToAddress("0x83A948D6bd3641Cf772c2eE811baeA64F1c381fF"))
}

func verify40(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventSwapOut
	if err := getRow(db, bsdb.EventSwapOutTableName, &e, "storage_provider_id = ?", 9); err != nil {
		return err
	}
	return errors.Join(
		eq("successor_sp_id", e.SuccessorSpId, 3),
		eq("global_virtual_group_family_id", e.GlobalVirtualGroupFamilyId, 0),
		eq("global_virtual_group_ids", fmt.Sprint(e.GlobalVirtualGroupIds), "[10]"),
	)
}

func verify41(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventCompleteSwapOut
	if err := getRow(db, bsdb.EventCompleteSwapOutTableName, &e, "src_storage_provider_id = ?", 9); err != nil {
		return err
	}
	return errors.Join(
		eq("storage_provider_id", e.StorageProviderId, 3),
		eq("global_virtual_group_family_id", e.GlobalVirtualGroupFamilyId, 0),
		eq("global_virtual_group_ids", fmt.Sprint(e.GlobalVirtualGroupIds), "[10]"),
	)
}

func verify42(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventCompleteStorageProviderExit
	if err := getRow(db, bsdb.EventCompleteStorageProviderExitTableName, &e, "storage_provider_id = ?", 10); err != nil {
		return err
	}
	return errors.Join(
		eq("operator_address", e.OperatorAddress, common.HexToAddress("0xCcCFa75E5Ef1d5543Be088FDbD3dD0683b9d6f71")),
		eqBig("total_deposit", e.TotalDeposit, "10000000000000000000000"),
	)
}

func verify43(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table((&models.Group{}).TableName()).Where("account_id = ? and group_id = ?", common.HexToAddress("0x4d57d300AfaF9f407e26552965ce355786206cF4"), common.HexToHash("2")).Find(&g).Error; err != nil {
		return err
	}
	if g.ExpirationTime != expirationEndOfYear3023Unix {
		return errors.New("ExpirationTime err")
	}
	return nil
}

func verify44(t *testing.T, db *gorm.DB) error {
	var sp models.StorageProvider
	if err := getRow(db, (&models.StorageProvider{}).TableName(), &sp, "sp_id = ?", 14); err != nil {
		return err
	}
	return errors.Join(
		eq("update_time_sec", sp.UpdateTimeSec, 1694568473),
		eq("free_read_quota", sp.FreeReadQuota, 10000),
		eqBig("read_price", sp.ReadPrice, "1000000000000000000000"),
		eqBig("store_price", sp.StorePrice, "100000000000000000000000"),
	)
}

func verify45(t *testing.T, db *gorm.DB) error {
	return verifyGroup(db, 22, "n3susb02", "0x6a45dE47a2Cd53084b4793fca7C1e706B9f54ED1", "null")
}

func verify46(t *testing.T, db *gorm.DB) error {
	owner := common.HexToAddress("0x6a45dE47a2Cd53084b4793fca7C1e706B9f54ED1")
	var errs []error
	for _, member := range []string{"0x97885F0E3711F74Bb1F97357bc9086Aed2a007B7", "0x5870Af236E63beaEbbFa364f78FC7c8e70F0811f"} {
		var g models.Group
		if err := getRow(db, bsdb.GroupTableName, &g, "group_id = ? and account_id = ?", idHash(22), common.HexToAddress(member)); err != nil {
			return err
		}
		errs = append(errs,
			eq(member+" owner", g.Owner, owner),
			eq(member+" operator", g.Operator, owner),
			eq(member+" group_name", g.GroupName, "n3susb02"),
			eq(member+" expiration_time", g.ExpirationTime, maxGroupMemberExpirationUnix),
		)
	}
	return errors.Join(errs...)
}

func verify47(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table(bsdb.GroupTableName).Where("group_id = ? and account_id = ?", common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000016"), common.HexToAddress("0x5870Af236E63beaEbbFa364f78FC7c8e70F0811f")).Find(&g).Error; err != nil {
		return err
	}
	if g.ExpirationTime != maxGroupMemberExpirationUnix {
		return fmt.Errorf("member expiration time is error :%d", g.ExpirationTime)
	}
	return nil
}

func verify48(t *testing.T, db *gorm.DB) error {
	var g models.Group
	if err := db.Table(bsdb.GroupTableName).Where("group_id = ? and account_id = ?", common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000016"), common.HexToAddress("0x5870AF236E63BEAEBBFA364F78FC7C8E70F0811F")).Find(&g).Error; err != nil {
		return err
	}
	if !g.Removed {
		return errors.New("member is not removed")
	}
	return nil
}

func verify49(t *testing.T, db *gorm.DB) error {
	var p models.PaymentAccount
	if err := db.Table((&models.PaymentAccount{}).TableName()).Where("addr = ?", common.HexToAddress("0x68bd245Df652435321989b999F9F70Cd31281b66")).Find(&p).Error; err != nil {
		return err
	}
	if !p.Refundable {
		return fmt.Errorf("failed to update Refundable")
	}
	return nil
}

func verify50(t *testing.T, db *gorm.DB) error {
	var p models.PaymentAccount
	if err := getRow(db, (&models.PaymentAccount{}).TableName(), &p, "addr = ?", common.HexToAddress("0x68bd245Df652435321989b999F9F70Cd31281b66")); err != nil {
		return err
	}
	if p.Refundable {
		return fmt.Errorf("failed to update Refundable")
	}
	return nil
}

func verify51(t *testing.T, db *gorm.DB) error {
	var e bsdb.EventRejectMigrateBucket
	if err := getRow(db, bsdb.EventRejectMigrateTableName, &e, "bucket_name = ?", "onkz"); err != nil {
		return err
	}
	return errors.Join(
		eq("bucket_id", e.BucketID, idHash(4)),
		eq("operator", e.Operator, common.HexToAddress("0xD05E628eBB81A2516DCe142702e28E765d463BB5")),
	)
}

func verify52(t *testing.T, db *gorm.DB) error {
	return verifyTaggedBucket(db, "b4tag02", 2, tagsKey12)
}

func verify53(t *testing.T, db *gorm.DB) error {
	return verifyTaggedBucket(db, "b4tag04", 5, "null")
}

func verify54(t *testing.T, db *gorm.DB) error {
	return verifyTaggedBucket(db, "b4tag04", 5, tagsKey12Tag04)
}

func verify55(t *testing.T, db *gorm.DB) error {
	return verifyTaggedObject(db, "ot005obj002", 2, 5, tagsKey12)
}

func verify56(t *testing.T, db *gorm.DB) error {
	return verifyTaggedObject(db, "ot005obj004", 12, 7, "null")
}

func verify57(t *testing.T, db *gorm.DB) error {
	return verifyTaggedObject(db, "ot005obj004", 12, 7, tagsKey12Tag04)
}

func verify58(t *testing.T, db *gorm.DB) error {
	return verifyGroup(db, 2, "g001-02", tagsOwner, tagsKey12)
}

func verify59(t *testing.T, db *gorm.DB) error {
	return verifyGroup(db, 4, "g001-04", tagsOwner, "null")
}

func verify60(t *testing.T, db *gorm.DB) error {
	return verifyGroup(db, 4, "g001-04", tagsOwner, `{"tags": [{"key": "tag04", "value": "tag04_value"}]}`)
}

func verify61(t *testing.T, db *gorm.DB) error {
	var count int64
	if err := db.Table(GetPrefixesTableName("cxz")).Where("bucket_name = ? and is_folder = ? and full_name = ?", "cxz", true, "/coco/").Count(&count).Error; err != nil {
		return errors.New("event not found")
	}

	// 0 means the folder was not created, 2 or more that it was created repeatedly
	if count != 1 {
		return fmt.Errorf("failed to batch create slash prefix tree node: %d folders, want 1", count)
	}
	return nil
}

func verify62(t *testing.T, db *gorm.DB) error {
	var count int64
	if err := db.Table(GetPrefixesTableName("cxz")).Where("bucket_name = ? and is_folder = ? and path_name = ?", "cxz", true, "/coco/").Count(&count).Error; err != nil {
		return errors.New("event not found")
	}

	// if count > 0 which means the /coco/ folder didn't delete successfully
	if count > 0 {
		return fmt.Errorf("failed to batch delete slash prefix tree node")
	}
	return verifyObjectsRemoved(db, 1201, 1202)
}

func verify63(t *testing.T, db *gorm.DB) error {
	var count int64
	if err := db.Table(GetPrefixesTableName("cxz")).Where("bucket_name = ? and is_folder = ? and path_name = ?", "cxz", true, "/sp/").Count(&count).Error; err != nil {
		return errors.New("event not found")
	}

	if count != 0 {
		return fmt.Errorf("delete and create same object in same block")
	}
	return verifyObjectsRemoved(db, 1301, 1302)
}

func verify64(t *testing.T, db *gorm.DB) error {
	var count int64
	if err := db.Table(GetPrefixesTableName("cxz")).Where("bucket_name = ? and full_name = ?", "cxz", "/coco/data/123.txt").Count(&count).Error; err != nil {
		return errors.New("event not found")
	}

	if count != 1 {
		return fmt.Errorf("delete and create same object in same block")
	}
	return nil
}

func verify65(t *testing.T, db *gorm.DB) error {
	var count int64
	if err := db.Table(GetPrefixesTableName("cxz")).Where("bucket_name = ? and full_name = ?", "cxz", "/coco/data/123.txt").Count(&count).Error; err != nil {
		return errors.New("event not found")
	}

	if count != 1 {
		return fmt.Errorf("delete and create same object in same block")
	}
	return nil
}

func verify66(t *testing.T, db *gorm.DB) error {
	table := GetObjectsTableName("self-custody-bucket-10")
	var o models.Object
	if err := db.Table(table).Where("bucket_name=? and object_name = ?", "self-custody-bucket-10", "28705261ca3b4146b2014af10612476e-5AF048C9-C68E-4266-BF23-40DDCA7F5897").Find(&o).Error; err != nil {
		assert.NoError(t, err)
	}
	if o.LocalVirtualGroupId != 10 {
		return errors.New("object status error")
	}

	return nil
}

func verify67(t *testing.T, db *gorm.DB) error {
	table := GetObjectsTableName("phugwly1grq")
	var o models.Object
	if err := db.Table(table).Where("bucket_name=? and object_name = ?", "phugwly1grq", "j5lgngcf").Find(&o).Error; err != nil {
		assert.NoError(t, err)
	}
	if o.Version != 1 {
		return errors.New("update object error")
	}
	if o.Status != "OBJECT_STATUS_SEALED" {
		return errors.New("update object error")
	}
	if o.IsUpdating {
		return errors.New("update object error")
	}
	return nil
}

func verify68(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table((&models.Bucket{}).TableName()).Where("bucket_name=?", "phugwly1grq").Find(&bucket).Error; err != nil {
		assert.NoError(t, err)
	}
	if !bucketmodules.IsStatusSet(bucket.OffChainStatus, int(bucketmodules.OffChainStatusIsLimited)) {
		return errors.New("update bucket flow rate limit status error")
	}
	return nil
}

func verify69(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := db.Table((&models.Bucket{}).TableName()).Where("bucket_name=?", "phugwly1grq").Find(&bucket).Error; err != nil {
		assert.NoError(t, err)
	}
	if !bucketmodules.IsStatusSet(bucket.OffChainStatus, int(bucketmodules.OffChainStatusSpAsDelegatedAgentDisabled)) {
		return errors.New("update bucket ToggleSPAsDelegatedAgent status error")
	}
	return nil
}

func verify70(t *testing.T, db *gorm.DB) error {
	var bucket models.Bucket
	if err := getRow(db, (&models.Bucket{}).TableName(), &bucket, "bucket_name=?", "phugwly1grq"); err != nil {
		return err
	}
	if bucketmodules.IsStatusSet(bucket.OffChainStatus, int(bucketmodules.OffChainStatusSpAsDelegatedAgentDisabled)) {
		return errors.New("update bucket ToggleSPAsDelegatedAgent status error")
	}
	return nil
}

const (
	ObjectsNumberOfShards = 64
	ObjectTableName       = "objects"
)

func GetObjectsTableName(bucketName string) string {
	return GetObjectsTableNameByShardNumber(int(GetObjectsShardNumberByBucketName(bucketName)))
}

func GetObjectsShardNumberByBucketName(bucketName string) uint32 {
	return murmur3.Sum32([]byte(bucketName)) % ObjectsNumberOfShards
}

func GetObjectsTableNameByShardNumber(shard int) string {
	return fmt.Sprintf("%s_%02d", ObjectTableName, shard)
}

const (
	PrefixesNumberOfShards = 64
	PrefixTreeTableName    = "slash_prefix_tree_nodes"
)

func GetPrefixesTableName(bucketName string) string {
	return GetPrefixesTableNameByShardNumber(int(GetPrefixesShardNumberByBucketName(bucketName)))
}

func GetPrefixesShardNumberByBucketName(bucketName string) uint32 {
	return murmur3.Sum32([]byte(bucketName)) % PrefixesNumberOfShards
}

func GetPrefixesTableNameByShardNumber(shard int) string {
	return fmt.Sprintf("%s_%02d", PrefixTreeTableName, shard)
}
