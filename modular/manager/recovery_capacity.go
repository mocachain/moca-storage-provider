package manager

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Claims survive timeouts and crashes. Terminal writers with confirmed exact
// staging cleanup may release their claims; orphan writers require operator cleanup.
// This conservative ledger bounds orphan writes without needing a store-wide List.
// The directory must be persistent, shared by successive single-active managers,
// and dedicated to this store. It is not a distributed lease.
func (m *ManageModular) reserveRecoveryCapacity(bytes uint64) (string, error) {
	dir := os.Getenv("RECOVERY_STAGING_LEDGER_DIR")
	limit, err := strconv.ParseUint(os.Getenv("RECOVERY_STAGING_CAPACITY_BYTES"), 10, 64)
	if err != nil || limit == 0 || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("persistent recovery staging ledger and capacity are required")
	}
	if bytes == 0 {
		bytes = 1
	}
	m.recoveryCapacityMu.Lock()
	defer m.recoveryCapacityMu.Unlock()
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var used uint64
	for _, entry := range entries {
		if entry.IsDir() {
			return "", fmt.Errorf("unexpected directory in recovery capacity ledger")
		}
		fields := strings.Split(entry.Name(), "-")
		if len(fields) != 2 {
			return "", fmt.Errorf("invalid recovery capacity claim")
		}
		n, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil || math.MaxUint64-used < n {
			return "", fmt.Errorf("invalid recovery capacity accounting")
		}
		used += n
	}
	if used > limit || bytes > limit-used {
		return "", fmt.Errorf("recovery staging capacity exhausted: reserved=%d requested=%d limit=%d", used, bytes, limit)
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return "", err
	}
	claim := filepath.Join(dir, fmt.Sprintf("%d-%s", bytes, hex.EncodeToString(token)))
	file, err := os.OpenFile(claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		return "", err
	}
	return claim, nil
}

// The caller must hold the attempt gate and prove every writer covered by the
// claim terminal, and every accepted staging key deleted. Never release orphans.
func (m *ManageModular) releaseRecoveryCapacity(claim string) error {
	if claim == "" {
		return nil
	}
	m.recoveryCapacityMu.Lock()
	defer m.recoveryCapacityMu.Unlock()
	if err := os.Remove(claim); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(claim))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
