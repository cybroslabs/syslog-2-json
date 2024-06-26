package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"net/http/pprof"
)

type Service struct {
	logger *zap.SugaredLogger

	alive bool
	ready bool
	rm    sync.RWMutex
}

func NewService(logger *zap.SugaredLogger) *Service {
	return &Service{
		logger: logger,
		alive:  true,  // we always serve the liveness probe true, we don't wanna be killed
		ready:  false, // service always starts as not ready, SetReady() must be called
	}
}

func (s *Service) Run(ctx context.Context, stop func(), wg *sync.WaitGroup, port int, logger *zap.SugaredLogger) {
	defer wg.Done()
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", s.livenessProbeHandler)
	mux.HandleFunc("/readyz", s.readinessProbeHandler)
	mux.HandleFunc("/startupz", s.startupProbeHandler)
	mux.Handle("/metrics", promhttp.Handler())

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadTimeout:       3 * time.Second,
		ReadHeaderTimeout: 3 * time.Second,
		WriteTimeout:      45 * time.Second,
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// if we fail to start, we just stop the program
			logger.Fatal("Failed to start service http server (probes, metrics)")
		}
	}()

	logger.Infof("Service HTTP server (probes and metrics) started on port %v", port)

	<-ctx.Done()

	logger.Info("Shutting down service HTTP server (probes and metrics)...")

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(timeoutCtx); err != nil {
		logger.Errorf("Failed to shut down service HTTP server (probes and metrics): %v", err)
	} else {
		logger.Info("Service HTTP server (probes and metrics) has shut down")
	}
}

func (s *Service) SetNotReady() {
	s.rm.Lock()
	defer s.rm.Unlock()
	s.ready = false
}

func (s *Service) SetReady() {
	s.rm.Lock()
	defer s.rm.Unlock()
	s.ready = true
}

func (s *Service) Ready() bool {
	s.rm.RLock()
	defer s.rm.RUnlock()
	return s.ready
}

func (s *Service) SetAlive() {
	s.alive = true
}

func (s *Service) livenessProbeHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.alive {
		_, err := w.Write([]byte("OK"))
		if err != nil {
			s.logger.Error("Error writing response")
		}

		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
}

func (s *Service) readinessProbeHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.Ready() {
		_, err := w.Write([]byte("OK"))
		if err != nil {
			s.logger.Error("Error writing response")
		}

		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
}

func (s *Service) startupProbeHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.Ready() {
		_, err := w.Write([]byte("OK"))
		if err != nil {
			s.logger.Error("Error writing response")
		}

		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
}
