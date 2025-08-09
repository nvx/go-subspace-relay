package subspacerelay

import (
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"log/slog"
	"os"
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
