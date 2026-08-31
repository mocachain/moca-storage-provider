package util

import (
	"fmt"
	"os"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// SecretFromEnv resolves a secret that may come either from the configuration file
// or from an environment variable.
//
// An unset variable keeps the configured value. A variable that is set but empty is
// rejected: an empty secret is a deployment mistake, not an instruction to start
// without one. When the environment does supply a value, the source is recorded in
// the log — and, if it replaces a configured value, that is recorded as a warning,
// so an operator can tell which secret the process actually started with. The secret
// itself is never logged.
func SecretFromEnv(env, configured string) (string, error) {
	val, ok := os.LookupEnv(env)
	if !ok {
		return configured, nil
	}
	if val == "" {
		return "", fmt.Errorf("env %s is set but empty", env)
	}
	if configured != "" && configured != val {
		log.Warnw("the configured secret is replaced by the environment", "env", env)
	} else {
		log.Infow("the secret is taken from the environment", "env", env)
	}
	return val, nil
}
