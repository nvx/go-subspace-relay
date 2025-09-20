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

func RelayInfoAttrs(relayInfo *subspacerelaypb.RelayInfo) slog.Attr {
	attrs := []slog.Attr{
		slog.String("connection_type", relayInfo.ConnectionType.String()),
	}
	if relayInfo.DeviceName != "" {
		attrs = append(attrs, slog.String("device_name", relayInfo.DeviceName))
	}
	if len(relayInfo.Atr) != 0 {
		attrs = append(attrs, slog.String("atr", strings.ToUpper(hex.EncodeToString(relayInfo.Atr))))
	}
	if len(relayInfo.DeviceAddress) != 0 {
		attrs = append(attrs, slog.String("device_address", strings.ToUpper(hex.EncodeToString(relayInfo.DeviceAddress))))
	}
	if relayInfo.Rssi != 0 {
		attrs = append(attrs, slog.Int("rssi", int(relayInfo.Rssi)))
	}

	return slog.GroupAttrs("relay_info", attrs...)
}
