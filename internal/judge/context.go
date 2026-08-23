package judge

import "context"

type workerIDContextKey struct{}

func WithWorkerID(ctx context.Context, workerID int) context.Context {
	return context.WithValue(ctx, workerIDContextKey{}, workerID)
}

func WorkerID(ctx context.Context) int {
	workerID, _ := ctx.Value(workerIDContextKey{}).(int)
	return workerID
}
