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

	"github.com/cybroslabs/syslog-2-json/internal/service"
	"github.com/libp2p/go-reuseport"
	"go.uber.org/zap"

	"github.com/influxdata/go-syslog/v3"
	"github.com/influxdata/go-syslog/v3/rfc3164"
	"github.com/influxdata/go-syslog/v3/rfc5424"
)

var (
	errFailedToGetSyslogData = errors.New("failed to get syslog data")
)

type Syslog2Json struct {
	logger *zap.SugaredLogger

	tcpListener *net.Listener
	udpListener *net.PacketConn
}

func (sluj *Syslog2Json) TcpHandler(ctx context.Context, wg *sync.WaitGroup, port int) {
	defer wg.Done()

	listener, err := reuseport.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		sluj.logger.Errorf("TCP listen failed: %v", err)
		return
	}
	sluj.tcpListener = &listener
	defer func() { _ = listener.Close() }()

	for {
		// Wait for a connection.
		conn, err := listener.Accept()
		if err != nil {
			sluj.logger.Errorf("TCP accept failed: %v", err)
			return
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
				if n > 0 {
					if err_handle := sluj.HandleSyslogMessage(c.RemoteAddr(), buf[:n]); err_handle != nil {
						sluj.logger.Errorf("Error %v for raw[%v]: %v", err_handle, n, string(buf[:n]))
					}
				}
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					sluj.logger.Error("TCP read failed: %v", err)
					break
				}
			}
		}(conn)
	}
}

func (sluj *Syslog2Json) UdpHandler(ctx context.Context, wg *sync.WaitGroup, port int) {
	defer wg.Done()

	listener, err := reuseport.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		sluj.logger.Errorf("UDP listen failed: %v", err)
		return
	}
	sluj.udpListener = &listener
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
			if err_handle := sluj.HandleSyslogMessage(addr, buf[:n]); err_handle != nil {
				sluj.logger.Errorf("Error %v for raw[%v]: %v", err_handle, n, string(buf[:n]))
			}
		}
		if err != nil {
			sluj.logger.Errorf("UDP read failed: %v", err)
			return
		}
	}
}

func (sluj *Syslog2Json) Close() {
	if sluj.tcpListener != nil {
		_ = (*sluj.tcpListener).Close()
	}
	if sluj.udpListener != nil {
		_ = (*sluj.udpListener).Close()
	}
}

func (sluj *Syslog2Json) filterArgs(data *syslog.Base, extended *rfc5424.SyslogMessage) []string {
	r := []string{}
	if data == nil {
		return r
	}

	if data.Facility != nil {
		r = append(r, "Facility", fmt.Sprintf("%d", *data.Facility))
	}
	if data.Severity != nil {
		r = append(r, "Severity", fmt.Sprintf("%d", *data.Severity))
	}
	if data.Priority != nil {
		r = append(r, "Priority", fmt.Sprintf("%d", *data.Priority))
	}
	if data.Timestamp != nil {
		r = append(r, "Timestamp", fmt.Sprintf("%v", *data.Timestamp))
	}
	if data.Hostname != nil {
		r = append(r, "Hostname", *data.Hostname)
	}
	if data.Appname != nil {
		r = append(r, "Appname", *data.Appname)
	}
	if data.ProcID != nil {
		r = append(r, "ProcID", *data.ProcID)
	}
	if data.MsgID != nil {
		r = append(r, "MsgID", *data.MsgID)
	}

	if extended != nil {
		structured_data := extended.StructuredData
		if structured_data != nil {
			for k, v := range *structured_data {
				if v == nil {
					continue
				}
				r = append(
					r,
					k,
					fmt.Sprintf("%v", v),
				)
			}
		}
	}
	return r
}

func (sluj *Syslog2Json) HandleSyslogMessage(addr net.Addr, msg []byte) error {
	// Try to parse the message as RFC5424 first
	p_new := rfc5424.NewParser(rfc5424.WithBestEffort())
	m, err := p_new.Parse(msg)
	if err == nil {
		sm := m.(*rfc5424.SyslogMessage)
		if sm == nil {
			return errFailedToGetSyslogData
		}

		message := ""
		if sm.Message != nil {
			message = *sm.Message
		}

		// TODO: Level or so?

		sluj.logger.Infow(message, sluj.filterArgs(m.(*syslog.Base), sm))
		return nil
	}

	// If it fails, try to parse it as RFC3164
	p_old := rfc3164.NewParser(rfc3164.WithRFC3339(), rfc3164.WithBestEffort())
	m, err = p_old.Parse(msg)
	if err == nil {
		sm := m.(*rfc3164.SyslogMessage)
		if sm == nil {
			return errFailedToGetSyslogData
		}

		message := ""
		if sm.Message != nil {
			message = *sm.Message
		}

		// TODO: Level or so?

		sluj.logger.Infow(message, sluj.filterArgs(m.(*syslog.Base), nil))
		return nil
	}

	return err
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

	wg_subroutines := &sync.WaitGroup{}
	wg_probes := &sync.WaitGroup{}
	handlers := &Syslog2Json{
		logger: logger,
	}

	// Service and internal HTTP server (probes and metrics)
	ctx_service, cancel_service := context.WithCancel(context.Background())
	defer cancel_service()

	wg_probes.Add(1)
	go svc.Start(ctx_service, wg_probes, 8090, logger)

	wg_subroutines.Add(1)
	go func(ctx context.Context, tcpPort int, logger *zap.SugaredLogger) {
		defer logger.Info("TCP handler stopped")
		defer stop()
		handlers.TcpHandler(ctx, wg_subroutines, tcpPort)
	}(ctx, port, logger)

	wg_subroutines.Add(1)
	go func(ctx context.Context, udpPort int, logger *zap.SugaredLogger) {
		defer logger.Info("UDP handler stopped")
		defer stop()
		handlers.UdpHandler(ctx, wg_subroutines, udpPort)
	}(ctx, port, logger)

	svc.SetReady()

	// Wait for a signal to stop the program
	<-ctx.Done()
	logger.Info("Shutting down syslog2json...")

	// Wait till all subroutines are done
	svc.SetNotReady()
	handlers.Close()
	wg_subroutines.Wait()

	// Stop the probe service
	cancel_service()
	wg_probes.Wait()
}
