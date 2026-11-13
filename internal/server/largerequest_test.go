package server

import (
	"testing"
	"time"
)

// TestLargeRequestGate 大请求排除窗（legacy 路径）的核心语义。
func TestLargeRequestGate(t *testing.T) {
	g := newLargeRequestGate()

	if g.isLarge(0, 10<<20) {
		t.Error("threshold=0 应关闭功能")
	}
	if !g.isLarge(256*1024, 256*1024) {
		t.Error("等于阈值应算大请求")
	}
	if g.isLarge(256*1024, 255*1024) {
		t.Error("小于阈值不算")
	}

	if !g.acquire("u1", 6, 90*time.Second) { t.Fatal("acquire 应成功") }
	ex := g.excluded(90 * time.Second)
	if !ex["u1"] {
		t.Error("窗口内应排除 u1")
	}
	time.Sleep(2 * time.Millisecond)
	ex = g.excluded(1 * time.Nanosecond)
	if ex["u1"] {
		t.Error("窗口过期后不应排除")
	}
}

// TestLargeBudget 预算制（#384 终版）核心语义：
// 窗口内计数 < 预算 → 不排除；达预算 → 排除；窗口滑过 → 自动恢复。
func TestLargeBudget(t *testing.T) {
	g := newLargeRequestGate()
	const budget = 6
	const window = 120 * time.Second

	// 5 条 acquire：未达预算
	for i := 0; i < 5; i++ {
		if !g.acquire("u1", budget, window) { t.Fatal("acquire 应成功（未超预算）") }
	}
	if g.budgetExceeded("u1", budget, window) {
		t.Error("5 条 < 预算 6，不该报超")
	}
	if g.excludedBudget(budget, window)["u1"] {
		t.Error("5 条不应进排除集")
	}

	// 第 6 条：达预算
	if !g.acquire("u1", budget, window) { t.Fatal("acquire 应成功（未超预算）") }
	if !g.budgetExceeded("u1", budget, window) {
		t.Error("6 条 = 预算，应报超")
	}
	if !g.excludedBudget(budget, window)["u1"] {
		t.Error("达预算后应进排除集")
	}

	// 其他号不受影响
	if !g.acquire("u2", budget, window) { t.Fatal("acquire u2 应成功") }
	if g.budgetExceeded("u2", budget, window) {
		t.Error("u2 独立计数")
	}
}

// TestLargeBudgetWindowSlides 滑动窗口：旧时间戳滑出后计数回落，号自动恢复承接资格。
func TestLargeBudgetWindowSlides(t *testing.T) {
	g := newLargeRequestGate()
	const budget = 2
	const window = 50 * time.Millisecond

	if !g.acquire("u1", budget, window) { t.Fatal("acquire 应成功（未超预算）") }
	if !g.acquire("u1", budget, window) { t.Fatal("acquire 应成功（未超预算）") }
	if !g.budgetExceeded("u1", budget, window) {
		t.Fatal("2 条 = 预算，应报超")
	}
	time.Sleep(60 * time.Millisecond) // 窗口滑过
	if g.budgetExceeded("u1", budget, window) {
		t.Error("窗口滑过后应自动恢复")
	}
	if g.excludedBudget(budget, window)["u1"] {
		t.Error("恢复后不该在排除集")
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
	same := mergeExcluded(tried, nil)
	if len(same) != 2 {
		t.Errorf("空 large 应原样返回，len=%d", len(same))
	}
}