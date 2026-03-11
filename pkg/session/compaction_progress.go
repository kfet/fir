package session

import "context"

// CompactionProgressFunc is called during compaction to report streaming
// progress from the summarization LLM. phase names what is being done (e.g.
// "summarizing history", "summarizing turn context"). delta is the incremental
// text produced by the LLM so the caller can accumulate it or count tokens.
type CompactionProgressFunc func(phase, delta string)

type compactionProgressKey struct{}

// WithCompactionProgress returns a context carrying the given progress function.
func WithCompactionProgress(ctx context.Context, fn CompactionProgressFunc) context.Context {
	return context.WithValue(ctx, compactionProgressKey{}, fn)
}

// CompactionProgressFromCtx extracts the progress function from ctx, or returns nil.
func CompactionProgressFromCtx(ctx context.Context) CompactionProgressFunc {
	if fn, ok := ctx.Value(compactionProgressKey{}).(CompactionProgressFunc); ok {
		return fn
	}
	return nil
}
