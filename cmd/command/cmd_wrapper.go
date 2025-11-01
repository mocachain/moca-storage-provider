package command

import (
	"github.com/urfave/cli/v2"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	"github.com/mocachain/moca-storage-provider/cmd/utils"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
)

var CW CMDWrapper

// CMDWrapper defines cmd wrapper.
type CMDWrapper struct {
	config   *gfspconfig.GfSpConfig
	grpcAPI  gfspclient.GfSpClientAPI
	spDBAPI  spdb.SPDB
	chainAPI consensus.Consensus
}

func (w *CMDWrapper) init(ctx *cli.Context) (err error) {
	if w.config == nil || w.grpcAPI == nil || w.spDBAPI == nil {
		w.config, err = utils.MakeConfig(ctx)
		if err != nil {
			return err
		}
		w.grpcAPI = utils.MakeGfSpClient(w.config)
		w.spDBAPI, _ = utils.MakeSPDB(w.config)
	}
	return nil
}

func (w *CMDWrapper) initChainAPI(ctx *cli.Context) (err error) {
	if w.chainAPI == nil {
		config, configErr := utils.MakeConfig(ctx)
		if configErr != nil {
			return configErr
		}
		w.chainAPI, err = utils.MakeGnfd(config)
		return err
	}
	return nil
}

func (w *CMDWrapper) initEmptyGRPCAPI() {
	if w.grpcAPI == nil {
		w.grpcAPI = &gfspclient.GfSpClient{}
	}
}
