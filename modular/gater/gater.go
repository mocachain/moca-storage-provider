package gater

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/core/rcmgr"
	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca-storage-provider/pkg/metrics"
)

const (
	ReadHeaderTimeout        = 20 * time.Minute
	AuthNonceCleanupInterval = time.Hour
)

var _ module.Modular = &GateModular{}

type GateModular struct {
	env         string
	domain      string
	httpAddress string
	baseApp     *gfspapp.GfSpBaseApp
	scope       rcmgr.ResourceScope
	httpServer  *http.Server

	maxListReadQuota int64
	maxPayloadSize   uint64

	// statusAllowedAccounts holds the lower-cased accounts allowed to read the
	// operational status endpoint; an empty map closes it to everyone.
	statusAllowedAccounts map[string]struct{}

	// requireAuthNonce rejects authenticated requests without a nonce once
	// every client ships one.
	requireAuthNonce bool

	spID        uint32
	spCachePool *SPCachePool
}

func (g *GateModular) Name() string {
	return module.GateModularName
}

func (g *GateModular) Start(ctx context.Context) error {
	scope, err := g.baseApp.ResourceManager().OpenService(g.Name())
	if err != nil {
		return err
	}
	g.scope = scope
	go g.server(ctx)
	go g.cleanupAuthNonces(ctx, AuthNonceCleanupInterval)
	g.spCachePool = NewSPCachePool(g.baseApp.Consensus())
	return nil
}

func (g *GateModular) cleanupAuthNonces(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case expiredBefore := <-ticker.C:
			if err := g.baseApp.GfSpDB().DeleteExpiredAuthNonces(expiredBefore); err != nil {
				log.CtxErrorw(ctx, "failed to delete expired auth nonces", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (g *GateModular) server(ctx context.Context) {
	router := mux.NewRouter().SkipClean(true)
	if g.baseApp.EnableMetrics() {
		router.Use(metrics.DefaultHTTPServerMetrics.InstrumentationHandler)
	}
	g.RegisterHandler(router)
	g.httpServer = &http.Server{
		Addr:              g.httpAddress,
		Handler:           router,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadHeaderTimeout,
		WriteTimeout:      ReadHeaderTimeout,
		IdleTimeout:       time.Minute,
	}
	if err := g.httpServer.ListenAndServe(); err != nil {
		log.Errorw("failed to listen", "error", err)
		return
	}
}

func (g *GateModular) Stop(ctx context.Context) error {
	g.scope.Release()
	_ = g.httpServer.Shutdown(ctx)
	return nil
}

func (g *GateModular) ReserveResource(ctx context.Context, state *rcmgr.ScopeStat) (rcmgr.ResourceScopeSpan, error) {
	span, err := g.scope.BeginSpan()
	if err != nil {
		return nil, err
	}
	err = span.ReserveResources(state)
	if err != nil {
		return nil, err
	}
	return span, nil
}

func (g *GateModular) ReleaseResource(ctx context.Context, span rcmgr.ResourceScopeSpan) {
	span.Done()
}

func (g *GateModular) getSPID() (uint32, error) {
	if g.spID != 0 {
		return g.spID, nil
	}
	spInfo, err := g.baseApp.Consensus().QuerySP(context.Background(), g.baseApp.OperatorAddress())
	if err != nil {
		return 0, err
	}
	g.spID = spInfo.GetId()
	return g.spID, nil
}
