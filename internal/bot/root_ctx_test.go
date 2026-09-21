package bot

// Root-context derivation (roadmap 4.7, pragmatic scope): handlerCtx derives
// its per-handler 30s timeout from the process-lifetime root installed by
// SetRootContext, so shutdown cancellation reaches in-flight handler DB work.
// The nil fallback keeps every direct/test constructor working unchanged.

import (
	"context"
	"testing"
	"time"
)

// TestHandlerCtxDerivesFromRootContext pins both halves of the derivation on
// a zero-value Bot (handlerCtx touches nothing else):
//   - unset root: a working ctx whose deadline is within (29s, 30s];
//   - cancelled root: handlerCtx returns an already-cancelled ctx, i.e. the
//     process signal reaches in-flight handler work immediately.
func TestHandlerCtxDerivesFromRootContext(t *testing.T) {
	b := &Bot{}

	ctx, cancel := b.handlerCtx()
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("unset root: handlerCtx already cancelled: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unset root: handlerCtx has no deadline")
	}
	if d := time.Until(deadline); d <= 29*time.Second || d > 30*time.Second {
		t.Fatalf("unset root: deadline in %v, want within (29s, 30s]", d)
	}

	root, cancelRoot := context.WithCancel(context.Background())
	cancelRoot()
	b.SetRootContext(root)

	ctx2, cancel2 := b.handlerCtx()
	defer cancel2()
	if ctx2.Err() == nil {
		t.Fatal("cancelled root: handlerCtx not cancelled (shutdown must reach handler work)")
	}

	// An explicitly nil root is safe: the background fallback applies.
	b.SetRootContext(nil)
	ctx3, cancel3 := b.handlerCtx()
	defer cancel3()
	if ctx3.Err() != nil {
		t.Fatalf("nil root: handlerCtx cancelled: %v", ctx3.Err())
	}
}
