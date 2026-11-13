package scheduler

import (
	"testing"
	"time"
)

// TestJitterPermille 人味化抖动：稳定（同 key 同结果）、随 key 分散、边界正确、
// permill=0 / base<=0 时原样返回（功能关闭语义）。
func TestJitterPermille(t *testing.T) {
	base := 5 * time.Minute

	// permill=0 → 原样
	if got := jitterPermille(base, 0, "bal|u1"); got != base {
		t.Errorf("permill=0 应原样返回，got=%v", got)
	}
	// base<=0 → 原样
	if got := jitterPermille(0, 200, "k"); got != 0 {
		t.Errorf("base=0 应返回 0，got=%v", got)
	}

	// 稳定性：同 key 多次调用结果一致
	first := jitterPermille(base, 200, "bal|u1")
	for i := 0; i < 10; i++ {
		if got := jitterPermille(base, 200, "bal|u1"); got != first {
			t.Fatalf("同 key 应稳定，第 %d 次得到 %v ≠ %v", i, got, first)
		}
	}

	// 分散性：不同 key 大概率不同偏移
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[jitterPermille(base, 200, "bal|u"+string(rune('a'+i)))] = true
	}
	if len(seen) < 3 {
		t.Errorf("不同 key 应产生不同偏移，实际只 %d 种", len(seen))
	}

	// 范围：±200‰ × 5min = ±1min → [4min, 6min]
	for i := 0; i < 50; i++ {
		got := jitterPermille(base, 200, "range"+string(rune('a'+i)))
		if got < 4*time.Minute || got > 6*time.Minute {
			t.Fatalf("偏移越界: %v 不在 [4m,6m]", got)
		}
	}
}

// TestSpreadInterval 账号摊开：范围 [0, window)、稳定、关闭时为 0。
func TestSpreadInterval(t *testing.T) {
	window := time.Minute
	if got := spreadInterval(window, 0, "k"); got != 0 {
		t.Errorf("permill=0 应返回 0")
	}
	if got := spreadInterval(0, 200, "k"); got != 0 {
		t.Errorf("window=0 应返回 0")
	}
	first := spreadInterval(window, 200, "bal|u1")
	if first >= window {
		t.Errorf("摊开应 < window，got=%v", first)
	}
	if got := spreadInterval(window, 200, "bal|u1"); got != first {
		t.Errorf("同 key 应稳定")
	}
}

// TestSetJitterBounds setter 的钳制：负值归零、超上限归 500。
func TestSetJitterBounds(t *testing.T) {
	s := New(Config{})
	s.SetJitter(-5, 0)
	if got := s.jitterPermill.Load(); got != 0 {
		t.Errorf("负值应归 0，got=%d", got)
	}
	s.SetJitter(999, 0)
	if got := s.jitterPermill.Load(); got != 500 {
		t.Errorf("超上限应钳 500，got=%d", got)
	}
}
