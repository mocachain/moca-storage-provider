package sqldb

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const authNonceTestDSNEnv = "MOCA_NONCE_TEST_DSN"

func setupAuthNonceStores(t *testing.T) (*SpDBImpl, *SpDBImpl) {
	t.Helper()
	dsn := os.Getenv(authNonceTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run MySQL nonce store tests", authNonceTestDSNEnv)
	}

	open := func() *gorm.DB {
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		return db
	}
	firstDB := open()
	require.NoError(t, firstDB.Migrator().DropTable(&AuthNonceClaimTable{}))
	require.NoError(t, firstDB.AutoMigrate(&AuthNonceClaimTable{}))
	secondDB := open()

	t.Cleanup(func() {
		_ = firstDB.Migrator().DropTable(&AuthNonceClaimTable{})
		for _, db := range []*gorm.DB{firstDB, secondDB} {
			sqlDB, err := db.DB()
			if err == nil {
				_ = sqlDB.Close()
			}
		}
	})
	return &SpDBImpl{db: firstDB}, &SpDBImpl{db: secondDB}
}

func TestClaimAuthNonceFirstClaim(t *testing.T) {
	store, _ := setupAuthNonceStores(t)

	claimed, err := store.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))

	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestClaimAuthNonceRejectsDuplicate(t *testing.T) {
	store, _ := setupAuthNonceStores(t)
	expiry := time.Now().Add(time.Hour)
	requireClaimedAuthNonce(t, store, "0xAbCd", validStoreAuthNonce, expiry)

	claimed, err := store.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, expiry)

	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestClaimAuthNonceRejectsLiveDuplicateAcrossClientTimeZones(t *testing.T) {
	dsn := os.Getenv(authNonceTestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run MySQL nonce store tests", authNonceTestDSNEnv)
	}
	dsnConfig, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	dsnConfig.Loc, err = time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	firstDB, err := gorm.Open(mysql.Open(dsnConfig.FormatDSN()), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, firstDB.Migrator().DropTable(&AuthNonceClaimTable{}))
	require.NoError(t, firstDB.AutoMigrate(&AuthNonceClaimTable{}))
	dsnConfig.Loc = time.UTC
	secondDB, err := gorm.Open(mysql.Open(dsnConfig.FormatDSN()), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = firstDB.Migrator().DropTable(&AuthNonceClaimTable{})
		for _, db := range []*gorm.DB{firstDB, secondDB} {
			sqlDB, dbErr := db.DB()
			if dbErr == nil {
				_ = sqlDB.Close()
			}
		}
	})
	first := &SpDBImpl{db: firstDB}
	second := &SpDBImpl{db: secondDB}
	requireClaimedAuthNonce(t, first, "0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))

	claimed, err := second.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))

	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestClaimAuthNonceReplacesExpiredClaim(t *testing.T) {
	store, _ := setupAuthNonceStores(t)
	requireClaimedAuthNonce(t, store, "0xAbCd", validStoreAuthNonce, time.Now().Add(-time.Minute))

	claimed, err := store.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))

	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestClaimAuthNonceNormalizesAccountAndNonce(t *testing.T) {
	store, _ := setupAuthNonceStores(t)
	expiry := time.Now().Add(time.Hour)
	requireClaimedAuthNonce(t, store, " 0xAbCd ", "00112233445566778899AABBCCDDEEFF", expiry)

	claimed, err := store.ClaimAuthNonce("0xabcd", validStoreAuthNonce, expiry)

	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestClaimAuthNonceConcurrentDuplicateHasOneWinner(t *testing.T) {
	first, second := setupAuthNonceStores(t)
	stores := []*SpDBImpl{first, second}
	const callers = 24
	results := make(chan bool, callers)
	errors := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(store *SpDBImpl) {
			defer wg.Done()
			<-start
			claimed, err := store.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))
			results <- claimed
			errors <- err
		}(stores[i%len(stores)])
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)

	claimedCount := 0
	for claimed := range results {
		if claimed {
			claimedCount++
		}
	}
	for err := range errors {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, claimedCount)
}

func TestClaimAuthNoncePersistsAcrossStoreInstances(t *testing.T) {
	first, second := setupAuthNonceStores(t)
	expiry := time.Now().Add(time.Hour)
	requireClaimedAuthNonce(t, first, "0xAbCd", validStoreAuthNonce, expiry)

	claimed, err := second.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, expiry)

	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestDeleteExpiredAuthNonces(t *testing.T) {
	store, _ := setupAuthNonceStores(t)
	requireClaimedAuthNonce(t, store, "0xAbCd", validStoreAuthNonce, time.Now().Add(-time.Minute))
	requireClaimedAuthNonce(t, store, "0xAbCd", "ffeeddccbbaa99887766554433221100", time.Now().Add(time.Hour))

	require.NoError(t, store.DeleteExpiredAuthNonces(time.Now()))

	var claims []AuthNonceClaimTable
	require.NoError(t, store.db.Order("nonce_key").Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Contains(t, claims[0].NonceKey, "ffeeddccbbaa99887766554433221100")
}

func TestAuthNonceStoreErrors(t *testing.T) {
	store, _ := setupAuthNonceStores(t)
	sqlDB, err := store.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	claimed, claimErr := store.ClaimAuthNonce("0xAbCd", validStoreAuthNonce, time.Now().Add(time.Hour))
	assert.False(t, claimed)
	assert.Error(t, claimErr)
	assert.Error(t, store.DeleteExpiredAuthNonces(time.Now()))
}

const validStoreAuthNonce = "00112233445566778899aabbccddeeff"

func requireClaimedAuthNonce(t *testing.T, store *SpDBImpl, account, nonce string, expiry time.Time) {
	t.Helper()
	claimed, err := store.ClaimAuthNonce(account, nonce, expiry)
	require.NoError(t, err)
	require.True(t, claimed, fmt.Sprintf("expected %s/%s to be claimed", account, nonce))
}
