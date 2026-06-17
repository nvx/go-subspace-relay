package subspacerelay

import (
	"context"
	"fmt"
)

const (
	ContentTypeProto = "application/proto"
)

var (
	ErrRemoteDisconnect = fmt.Errorf("%w: remote triggered permanent disconnect", context.Canceled)
)
