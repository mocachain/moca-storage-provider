package authenticator

import (
	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	coremodule "github.com/mocachain/moca-storage-provider/core/module"
)

func NewAuthenticationModular(app *gfspapp.GfSpBaseApp, cfg *gfspconfig.GfSpConfig) (coremodule.Modular, error) {
	auth := &AuthenticationModular{baseApp: app}
	return auth, nil
}
