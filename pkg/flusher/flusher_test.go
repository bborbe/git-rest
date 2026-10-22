// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flusher_test

import (
	"context"
	"errors"
	"testing"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/flusher"
)

func TestRunReturnsWhenContextCancelled(t *testing.T) {
	fake := &mocks.FakeGit{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := flusher.New(fake, libtime.Duration(time.Millisecond)).Run(ctx); err == nil {
		t.Fatal("expected the cancelled context error")
	}
	if fake.FlushPendingIfDueCallCount() != 0 {
		t.Fatalf("want no flush calls, got %d", fake.FlushPendingIfDueCallCount())
	}
}

func TestRunTicksAndFlushes(t *testing.T) {
	fake := &mocks.FakeGit{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	if err := flusher.New(fake, libtime.Duration(5*time.Millisecond)).Run(ctx); err == nil {
		t.Fatal("expected the context deadline error")
	}
	if fake.FlushPendingIfDueCallCount() == 0 {
		t.Fatal("want at least one flush call from the ticker")
	}
}

func TestRunContinuesAfterFlushError(t *testing.T) {
	fake := &mocks.FakeGit{}
	fake.FlushPendingIfDueReturns(errors.New("flush failed"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = flusher.New(fake, libtime.Duration(5*time.Millisecond)).Run(ctx)

	if fake.FlushPendingIfDueCallCount() < 2 {
		t.Fatalf("want the loop to keep ticking after an error, got %d calls",
			fake.FlushPendingIfDueCallCount())
	}
}
