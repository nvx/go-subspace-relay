package subspacerelay

import "context"

func joinContextCancellation(cancellationParent, ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)

	stop := context.AfterFunc(cancellationParent, func() {
		cancel(context.Cause(cancellationParent))
	})

	return ctx, func() {
		stop()
		cancel(nil)
	}
}
