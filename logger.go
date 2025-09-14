package subspacerelay

import (
	"encoding/hex"
	subspacerelaypb "github.com/nvx/subspace-relay"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"log/slog"
	"os"
	"strings"
	"time"
)

func InitLogger(name string) {
	ec := prettyconsole.NewEncoderConfig()
	ec.EncodeTime = prettyconsole.DefaultTimeEncoder(time.RFC3339)
	ec.CallerKey = "zap_caller"
	ec.FunctionKey = "zap_function"
	zapCore := zapcore.NewCore(
		prettyconsole.NewEncoder(ec),
		zapcore.Lock(os.Stdout),
		zap.DebugLevel,
	)

	slog.SetLogLoggerLevel(slog.LevelInfo)

	slog.SetDefault(slog.New(zapslog.NewHandler(
		zapCore,
		zapslog.WithName(name),
		zapslog.AddStacktraceAt(slog.LevelError),
	)))
}

func ClientInfoAttrs(clientInfo *subspacerelaypb.ClientInfo) slog.Attr {
	attrs := []slog.Attr{
		slog.String("connection_type", clientInfo.ConnectionType.String()),
	}
	if clientInfo.DeviceName != "" {
		attrs = append(attrs, slog.String("device_name", clientInfo.DeviceName))
	}
	if len(clientInfo.Atr) != 0 {
		attrs = append(attrs, slog.String("atr", strings.ToUpper(hex.EncodeToString(clientInfo.Atr))))
	}
	if len(clientInfo.DeviceAddress) != 0 {
		attrs = append(attrs, slog.String("device_address", strings.ToUpper(hex.EncodeToString(clientInfo.DeviceAddress))))
	}
	if clientInfo.Rssi != 0 {
		attrs = append(attrs, slog.Int("rssi", int(clientInfo.Rssi)))
	}

	return slog.GroupAttrs("client_info", attrs...)
}
