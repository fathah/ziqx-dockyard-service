package process

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestJobLogRecordsMutatingCommandsOnly(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithJobLog(context.Background(), &Capped{W: &buf, Limit: 1 << 20})
	r := Exec{Timeout: 5 * time.Second, Limit: 1 << 16}
	if _, err := r.Run(ctx, "/bin/echo", "/", []string{"compose", "up", "started"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "/bin/echo", "/", []string{"compose", "ps", "quiet"}); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	if !strings.Contains(log, "$ echo compose up started") || !strings.Contains(log, "✓ done") || strings.Contains(log, "ps quiet") {
		t.Fatal(log)
	}
}

func TestCappedLogTruncates(t *testing.T) {
	var buf bytes.Buffer
	c := &Capped{W: &buf, Limit: 10}
	c.Write([]byte("0123456789abcdef"))
	c.Write([]byte("more"))
	if !strings.HasPrefix(buf.String(), "0123456789") || strings.Contains(buf.String(), "abc") || strings.Contains(buf.String(), "more") {
		t.Fatal(buf.String())
	}
}
