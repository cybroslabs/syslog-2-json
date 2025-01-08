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
	"go.uber.org/zap/zapcore"

	"github.com/influxdata/go-syslog/v3/rfc3164"
	"github.com/influxdata/go-syslog/v3/rfc5424"
)

var (
	errFailedToGetSyslogData = errors.New("failed to get syslog data")
)

type Syslog2Json struct {
	jsonLogger *zap.Logger
	logger     *zap.SugaredLogger

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
				for n > 0 {
					n_next := n - 1
					if buf[n_next] <= 32 {
						n--
					} else {
						if err_handle := sluj.HandleSyslogMessage(c.RemoteAddr(), buf[:n]); err_handle != nil {
							sluj.logger.Errorf("Error %v for raw[%v]: %v", err_handle, n, string(buf[:n]))
						}
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
		for n > 0 {
			n_next := n - 1
			if buf[n_next] <= 32 {
				n--
			} else {
			if err_handle := sluj.HandleSyslogMessage(addr, buf[:n]); err_handle != nil {
				sluj.logger.Errorf("Error %v for raw[%v]: %v", err_handle, n, string(buf[:n]))
			}
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

func (sluj *Syslog2Json) messageToArgsRfc5424(data *rfc5424.SyslogMessage) []zap.Field {
	r := make([]zap.Field, 0)
	if data == nil {
		return r
	}

	r = append(r, zap.Field{Key: "syslog", Type: zapcore.StringType, String: "RFC5424"})

	if data.Facility != nil {
		r = append(r, zap.Field{Key: "Facility", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Facility)})
	}
	if data.Severity != nil {
		r = append(r, zap.Field{Key: "Severity", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Severity)})
	}
	if data.Priority != nil {
		r = append(r, zap.Field{Key: "Priority", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Priority)})
	}
	if data.Timestamp != nil {
		r = append(r, zap.Field{Key: "Timestamp", Type: zapcore.StringType, String: fmt.Sprintf("%v", *data.Timestamp)})
	}
	if data.Hostname != nil {
		r = append(r, zap.Field{Key: "Hostname", Type: zapcore.StringType, String: *data.Hostname})
	}
	if data.Appname != nil {
		r = append(r, zap.Field{Key: "Appname", Type: zapcore.StringType, String: *data.Appname})
	}
	if data.ProcID != nil {
		r = append(r, zap.Field{Key: "ProcID", Type: zapcore.StringType, String: *data.ProcID})
	}
	if data.MsgID != nil {
		r = append(r, zap.Field{Key: "MsgID", Type: zapcore.StringType, String: *data.MsgID})
	}

	structured_data := data.StructuredData
	if structured_data != nil {
		for k, v := range *structured_data {
			if v == nil {
				continue
			}
			r = append(
				r,
				zap.Field{
					Key:    k,
					Type:   zapcore.StringType,
					String: fmt.Sprintf("%v", v),
				},
			)
		}
	}

	return r
}

func (sluj *Syslog2Json) messageToArgsRfc3164(data *rfc3164.SyslogMessage) []zap.Field {
	r := make([]zap.Field, 0)
	if data == nil {
		return r
	}

	r = append(r, zap.Field{Key: "syslog", Type: zapcore.StringType, String: "RFC3164"})

	if data.Facility != nil {
		r = append(r, zap.Field{Key: "Facility", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Facility)})
	}
	if data.Severity != nil {
		r = append(r, zap.Field{Key: "Severity", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Severity)})
	}
	if data.Priority != nil {
		r = append(r, zap.Field{Key: "Priority", Type: zapcore.StringType, String: fmt.Sprintf("%d", *data.Priority)})
	}
	if data.Timestamp != nil {
		r = append(r, zap.Field{Key: "Timestamp", Type: zapcore.StringType, String: fmt.Sprintf("%v", *data.Timestamp)})
	}
	if data.Hostname != nil {
		r = append(r, zap.Field{Key: "Hostname", Type: zapcore.StringType, String: *data.Hostname})
	}
	if data.Appname != nil {
		r = append(r, zap.Field{Key: "Appname", Type: zapcore.StringType, String: *data.Appname})
	}
	if data.ProcID != nil {
		r = append(r, zap.Field{Key: "ProcID", Type: zapcore.StringType, String: *data.ProcID})
	}
	if data.MsgID != nil {
		r = append(r, zap.Field{Key: "MsgID", Type: zapcore.StringType, String: *data.MsgID})
	}

	return r
}

func (sluj *Syslog2Json) log(message *string, severity *uint8, data []zap.Field) {
	iseverity := 6
	if severity != nil {
		iseverity = int(*severity)
	}

	imessage := ""
	if message != nil {
		imessage = *message
	}

	if len(imessage) == 0 && len(data) == 0 {
		return
	}

	switch iseverity {
	case 0, 1, 2, 3:
		sluj.jsonLogger.Error(imessage, data...)
	case 4:
		sluj.jsonLogger.Warn(imessage, data...)
	case 5, 6:
		sluj.jsonLogger.Info(imessage, data...)
	case 7:
		sluj.jsonLogger.Debug(imessage, data...)
	default:
		sluj.jsonLogger.Info(imessage, data...)
	}
}

var (
	parserRfc5424 = rfc5424.NewParser()
	parserRfc3164 = rfc3164.NewParser(rfc3164.WithRFC3339())
)

func (sluj *Syslog2Json) HandleSyslogMessage(addr net.Addr, msg []byte) error {
	// Try to parse the message as RFC5424 first
	m, err := parserRfc5424.Parse(msg)
	if (err == nil) && (m != nil) {
		sm := m.(*rfc5424.SyslogMessage)
		if sm == nil {
			return errFailedToGetSyslogData
		}
		sluj.log(sm.Message, sm.Severity, sluj.messageToArgsRfc5424(sm))
		return nil
	}

	// If it fails, try to parse it as RFC3164
	m, err = parserRfc3164.Parse(msg)
	if (err == nil) && (m != nil) {
		sm := m.(*rfc3164.SyslogMessage)
		if sm == nil {
			return errFailedToGetSyslogData
		}
		sluj.log(sm.Message, sm.Severity, sluj.messageToArgsRfc3164(sm))
		return nil
	}

	if err == nil {
		err = errFailedToGetSyslogData
	}

	return err
}

func main() {
	mainCtx, mainStop := context.WithCancel(context.Background())
	subroutinesCtx, subroutinesStop := context.WithCancel(mainCtx)
	defer mainStop()
	shutdownCtx, _ := signal.NotifyContext(subroutinesCtx, os.Interrupt, syscall.SIGTERM)

	zap_cfg := zap.NewProductionConfig()
	zap_cfg.DisableCaller = true
	zap_cfg.DisableStacktrace = true

	zap_logger, _ := zap_cfg.Build()
	defer func() { _ = zap_logger.Sync() }()
	logger := zap_logger.Sugar()

	port := 5141

	logger.Info("Initializing syslog2json...")

	svc := service.NewService(logger)

	handlers := &Syslog2Json{
		jsonLogger: zap_logger,
		logger:     logger,
	}

	wg := &sync.WaitGroup{}

	// Service and internal HTTP server (probes and metrics)
	wg.Add(1)
	go svc.Run(subroutinesCtx, subroutinesStop, wg, 8090, logger)

	wg.Add(1)
	go func(ctx context.Context, stop func(), tcpPort int, logger *zap.SugaredLogger) {
		defer logger.Info("TCP handler stopped")
		defer stop()
		handlers.TcpHandler(ctx, wg, tcpPort)
	}(mainCtx, mainStop, port, logger)

	wg.Add(1)
	go func(ctx context.Context, stop func(), udpPort int, logger *zap.SugaredLogger) {
		defer logger.Info("UDP handler stopped")
		defer stop()
		handlers.UdpHandler(ctx, wg, udpPort)
	}(mainCtx, mainStop, port, logger)

	// Set the service to ready
	svc.SetReady()

	// Wait for shutdown and or main stop event
	<-shutdownCtx.Done()
	shutdownBegan := time.Now()

	logger.Infof("Shutting down server gracefully...")

	// Set the service to not ready and give Kubernetes some time to stop sending traffic
	svc.SetNotReady()
	if mainCtx.Err() == nil {
		// If the mainCtx was not yet triggered then we shall keep 'not-ready' signal for a while
		time.Sleep(5 * time.Second)
	}

	// Stop the server (if it's still running)
	mainStop()

	// Wait for the server to stop
	wg.Wait()

	logger.Infof("Server shut down gracefully. Took %v", time.Since(shutdownBegan))
}
