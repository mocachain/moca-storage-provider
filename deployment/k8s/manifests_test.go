package k8s_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSignerManifestsDoNotInjectFundingKey(t *testing.T) {
	for _, path := range []string{"base/components/single/signer/component.yaml", "base/components/combo/all-in-one/component.yaml"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var deployment struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Env []struct {
									Name      string
									ValueFrom struct {
										SecretKeyRef struct{ Key string } `yaml:"secretKeyRef"`
									} `yaml:"valueFrom"`
								}
							}
						}
					}
				}
			}
			require.NoError(t, yaml.Unmarshal(data, &deployment))
			require.NotEmpty(t, deployment.Spec.Template.Spec.Containers)
			keys := map[string]string{}
			for _, container := range deployment.Spec.Template.Spec.Containers {
				for _, env := range container.Env {
					require.NotEqual(t, "SIGNER_FUNDING_PRIV_KEY", env.Name)
					require.NotEqual(t, "SIGNER_FUNDING_PRIV_KEY", env.ValueFrom.SecretKeyRef.Key)
					keys[env.Name] = env.ValueFrom.SecretKeyRef.Key
				}
			}
			for _, key := range []string{"SIGNER_OPERATOR_PRIV_KEY", "SIGNER_APPROVAL_PRIV_KEY", "SIGNER_SEAL_PRIV_KEY", "SIGNER_GC_PRIV_KEY"} {
				require.Equal(t, key, keys[key])
			}
			if path == "base/components/single/signer/component.yaml" {
				require.Equal(t, "SIGNER_BLS_PRIV_KEY", keys["SIGNER_BLS_PRIV_KEY"])
			}
		})
	}
}
