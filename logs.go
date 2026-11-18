package main

import (
	"fmt"
	"sync"
	"time"
)

type LogBuffer struct {
	mu    sync.RWMutex
	lines []string
}

func (b *LogBuffer) Add(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, fmt.Sprintf("%s  %s", time.Now().Format("15:04:05"), s))
	if len(b.lines) > 250 {
		b.lines = b.lines[len(b.lines)-250:]
	}
}
func (b *LogBuffer) List() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
