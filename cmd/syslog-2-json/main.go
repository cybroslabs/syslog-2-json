package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cybroslabs/syslog-2-json/internal/service"
	"github.com/libp2p/go-reuseport"
	"go.uber.org/zap"

	"github.com/influxdata/go-syslog/v3/rfc5424"
)

var (
	errFailedToGetSyslogData = errors.New("failed to get syslog data")
)

type Syslog2Json struct {
	logger *zap.SugaredLogger
}

func (sluj *Syslog2Json) TcpHandler(ctx context.Context, wg *sync.WaitGroup, port int) {
	defer wg.Done()

	listener, err := reuseport.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		sluj.logger.Fatalf("TCP listen failed: %v", err)
		return
	}
	defer func() { _ = listener.Close() }()

	for {
		// Wait for a connection.
		conn, err := listener.Accept()
		if err != nil {
			sluj.logger.Fatalf("TCP accept failed: %v", err)
		}
		// Handle the connection in a new goroutine.
		// The loop then returns to accepting, so that
		// multiple connections may be served concurrently.
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()

			buf := make([]byte, 2048)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				n, err := c.Read(buf)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					sluj.logger.Error("TCP read failed: %v", err)
					break
				}
				if n > 0 {
					if err = sluj.HandleSyslogMessage(c.RemoteAddr(), buf[:n]); err != nil {
						sluj.logger.Debug("Raw", buf[:n])
					}
				}
			}
		}(conn)
	}
}

func (sluj *Syslog2Json) UdpHandler(ctx context.Context, wg *sync.WaitGroup, port int) {
	defer wg.Done()

	listener, err := reuseport.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		sluj.logger.Fatalf("UDP listen failed: %v", err)
	}
	defer listener.Close()

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, addr, err := listener.ReadFrom(buf)
		if n > 0 {
			if err = sluj.HandleSyslogMessage(addr, buf[:n]); err != nil {
				sluj.logger.Debug("Raw", buf[:n])
			}
		}
		if err != nil {
			sluj.logger.Fatalf("UDP read failed: %v", err)
		}
	}
}

func (sluj *Syslog2Json) HandleSyslogMessage(addr net.Addr, msg []byte) error {
	p := rfc5424.NewParser(rfc5424.WithBestEffort())
	m, err := p.Parse(msg)
	if err != nil {
		return err
	}

	sm := m.(*rfc5424.SyslogMessage)
	if sm == nil {
		return errFailedToGetSyslogData
	}

	message := ""
	if sm.Message == nil {
		message = *sm.Message
	}

	// TODO: Level or so?

	sluj.logger.Infow(message, sm)

	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // os.Interrupt = syscall.SIGINT
	defer stop()

	zap_logger, _ := zap.NewProduction()
	defer func() { _ = zap_logger.Sync() }()
	logger := zap_logger.Sugar()

	port := 5141

	logger.Info("Initializing syslog2json...")

	svc := service.NewService(logger)
	svcShutdownDelay := 5 * time.Second

	wg := sync.WaitGroup{}
	handlers := &Syslog2Json{
		logger: logger,
	}

	// Service and internal HTTP server (probes and metrics)
	wg.Add(1)
	go svc.Start(ctx, &wg, 8090, svcShutdownDelay, logger)

	wg.Add(1)
	go func(ctx context.Context, tcpPort int) {
		handlers.TcpHandler(ctx, &wg, tcpPort)
	}(ctx, port)

	wg.Add(1)
	go func(ctx context.Context, udpPort int) {
		handlers.UdpHandler(ctx, &wg, udpPort)
	}(ctx, port)

	svc.SetReady()

	// Wait for a signal to stop the program
	<-ctx.Done()
	svc.SetNotReady()
	wg.Wait()
}
