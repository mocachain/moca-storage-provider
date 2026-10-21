package signer

import (
	"fmt"
	"os"
	"runtime/debug"
)

var (
	evmAuditActorValue  = defaultEvmAuditActor()
	evmToolVersionValue = defaultEvmToolVersion()
)

func defaultEvmAuditActor() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("signer/%s/%d", host, os.Getpid())
}

func defaultEvmToolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	version := info.Main.Version
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			version += "@" + setting.Value
		}
	}
	return version
}

func evmAuditActor() string  { return evmAuditActorValue }
func evmToolVersion() string { return evmToolVersionValue }
