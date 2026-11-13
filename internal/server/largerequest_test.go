package server

import (
	"testing"
	"time"
)

// TestLargeRequestGate 大请求排除窗的核心语义：isLarge 阈值判定、窗口过期、
// 全关闭（threshold<=0）。
func TestLargeRequestGate(t *testing.T) {
	g := newLargeRequestGate()

	// threshold<=0 = 功能关闭
	if g.isLarge(0, 10<<20) {
		t.Error("threshold=0 应关闭功能")
	}
	if g.isLarge(-1, 10<<20) {
		t.Error("threshold<0 应关闭功能")
	}
	// 阈值判定
	if !g.isLarge(256*1024, 256*1024) {
		t.Error("等于阈值应算大请求")
	}
	if !g.isLarge(256*1024, 300*1024) {
		t.Error("超阈值应算大请求")
	}
	if g.isLarge(256*1024, 255*1024) {
		t.Error("小于阈值不算")
	}

	// 排除窗：mark 后在窗口内被排除，过期后解除
	g.mark("u1")
	ex := g.excluded(90 * time.Second)
	if !ex["u1"] {
		t.Error("窗口内应排除 u1")
	}
	// 窗口极短 → 立即过期
	ex = g.excluded(1 * time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	ex = g.excluded(1 * time.Nanosecond)
	if ex["u1"] {
		t.Error("窗口过期后不应排除")
	}
}

// TestMergeExcluded 两组排除合并：tried 保留、large 并入、large 为空时不复制。
func TestMergeExcluded(t *testing.T) {
	tried := map[string]bool{"a": true, "b": true}
	large := map[string]bool{"c": true}
	out := mergeExcluded(tried, large)
	if !out["a"] || !out["b"] || !out["c"] {
		t.Errorf("merge 结果=%v", out)
	}
	// large 为空 → 原样返回（零拷贝路径）
	same := mergeExcluded(tried, nil)
	if len(same) != 2 {
		t.Errorf("空 large 应原样返回，len=%d", len(same))
	}
}
