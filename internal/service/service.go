package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

type Service struct {
	logger *zap.SugaredLogger

	alive bool
	ready bool
	rm    sync.RWMutex
	// TODO: add metrics store here
}

func NewService(logger *zap.SugaredLogger) *Service {
	return &Service{
		logger: logger,
		alive:  true,  // we always serve the liveness probe true, we don't wanna be killed
		ready:  false, // service always starts as not ready, SetReady() must be called
	}
}

func (s *Service) Start(ctx context.Context, wg *sync.WaitGroup, port int, shutdownDelay time.Duration, logger *zap.SugaredLogger) {
	defer wg.Done()

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", s.livenessProbeHandler)
	mux.HandleFunc("/readyz", s.readinessProbeHandler)
	mux.HandleFunc("/startupz", s.startupProbeHandler)
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadTimeout:       3 * time.Second,
		ReadHeaderTimeout: 3 * time.Second,
		WriteTimeout:      1 * time.Second,
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// if we fail to start, we just stop the program
			logger.Fatal("Failed to start service http server (probes, metrics)")
		}
	}()

	logger.Infof("Service HTTP server (probes and metrics) started on port %v", port)

	quit := make(chan os.Signal, 1)

	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	logger.Infof("Received signal %v, shutting down service HTTP server (probes and metrics) in %v", sig, shutdownDelay)
	time.Sleep(shutdownDelay)

	logger.Info("Shutting down service HTTP server (probes and metrics)...")

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(timeoutCtx); err != nil {
		logger.Errorf("Force to shutdown service HTTP server (probes and metrics): %v", err)
	}

	logger.Info("Service HTTP server (probes and metrics) has shut down")
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
