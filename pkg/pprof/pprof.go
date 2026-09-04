package pprof

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/felixge/fgprof"
	"github.com/gorilla/mux"

	coremodule "github.com/mocachain/moca-storage-provider/core/module"
	corercmgr "github.com/mocachain/moca-storage-provider/core/rcmgr"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

var PProfModularName = strings.ToLower("PProf")
var _ coremodule.Modular = &PProf{}

// PProf is used to analyse the performance sp service
type PProf struct {
	httpAddress string
	httpServer  *http.Server
}

// NewPProf returns an instance of pprof
func NewPProf(address string) *PProf {
	return &PProf{httpAddress: address}
}

// Name describes pprof service name
func (p *PProf) Name() string {
	return PProfModularName
}

// Start HTTP server
func (p *PProf) Start(ctx context.Context) error {
	go p.serve()
	return nil
}

// Stop HTTP server
func (p *PProf) Stop(ctx context.Context) error {
	var errs []error
	if err := p.httpServer.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if errs != nil {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

func (p *PProf) serve() {
	router := mux.NewRouter()
	p.registerProfiler(router)
	p.httpServer = &http.Server{
		Addr:              p.httpAddress,
		Handler:           router,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	if err := p.httpServer.ListenAndServe(); err != nil {
		log.Errorw("failed to listen and serve", "error", err)
		return
	}
}

func (p *PProf) ReserveResource(ctx context.Context, state *corercmgr.ScopeStat) (corercmgr.ResourceScopeSpan, error) {
	return &corercmgr.NullScope{}, nil
}

func (p *PProf) ReleaseResource(ctx context.Context, scope corercmgr.ResourceScopeSpan) {
	scope.Done()
}

const (
	// defaultProfileSeconds mirrors the default collection window of
	// net/http/pprof's duration-based endpoints.
	defaultProfileSeconds = 30
	// profileWriteGrace is added on top of the requested collection window so
	// the response body can still be written after collection finishes.
	profileWriteGrace = 30 * time.Second
)

// durationAware extends the connection write deadline for handlers that
// collect for a caller-chosen duration before writing anything: the
// server-wide WriteTimeout starts when the headers are read, so without the
// extension it terminates any profile at or above the timeout.
//
// net/http/pprof's Profile and Trace handlers already extend the deadline
// themselves (configureWriteDeadline, present in the go1.23.x line this
// module builds with), so only the third-party fgprof route needs this.
func durationAware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secs, err := strconv.ParseFloat(r.FormValue("seconds"), 64)
		if err != nil || secs <= 0 {
			secs = defaultProfileSeconds
		}
		deadline := time.Now().Add(time.Duration(secs*float64(time.Second)) + profileWriteGrace)
		if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
			log.Errorw("failed to extend the profile write deadline", "error", err)
		}
		next.ServeHTTP(w, r)
	})
}

func (p *PProf) registerProfiler(r *mux.Router) {
	r.HandleFunc("/debug/pprof/", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)
	r.Handle("/debug/fgprof", durationAware(fgprof.Handler()))

	// Manually add support for paths linked to by index page at /debug/pprof/
	r.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	r.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	r.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	r.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	r.Handle("/debug/pprof/block", pprof.Handler("block"))
	r.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
}
