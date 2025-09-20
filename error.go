package subspacerelay

import (
	"cmp"
	"fmt"
	"github.com/ansel1/merry/v2"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"log/slog"
	"slices"
)

func DeferWrap(err *error) {
	if err != nil {
		*err = merry.WrapSkipping(*err, 1)
	}
}

func ErrorAttrs(err error) slog.Attr {
	const (
		stack   = "stack"
		message = "message"
	)

	m := merry.Values(err)
	attrs := make([]slog.Attr, 0, 1+len(m))

	attrs = append(attrs, slog.String(message, err.Error()))
	for k, v := range m {
		ks := fmt.Sprint(k)
		if ks == stack {
			v = prettyconsole.FormattedStringValue(merry.Stacktrace(err))
		}
		attrs = append(attrs, slog.Any(ks, v))
	}

	slices.SortFunc(attrs, func(a, b slog.Attr) int {
		if a.Key == message {
			return -1
		}
		if b.Key == message {
			return 1
		}
		if a.Key == stack {
			return 1
		}
		if b.Key == stack {
			return -1
		}
		return cmp.Compare(a.Key, b.Key)
	})

	return slog.Attr{Key: "error", Value: slog.GroupValue(attrs...)}
}
