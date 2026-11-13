package server

import (
	"sync"
	"time"
)

// largeRequestGate 大上下文请求的**账号级预算**（ithtelab #384 终版方案）。
//
// 背景：单号在短时间内被连续压超大上下文请求（DSH 长上下文 2.4~6MB）会触发
// 上游 11140 "unusual activity" 风控，把整批号封掉。但ithtelab #384 用 6473
// 条真实样本证明：被封与不被封的账号，「120s 窗口内 >50 万 token 请求的最大
// 条数」是同量级的（14~18 条）——**逐请求轮换的保护效果无法证实**，而换号的
// 代价（上游前缀缓存按账号存放，换号 = 整段上下文缓存未命中，费用显著升高）
// 却是确定的。
//
// 终版语义（粘性不动 + 账号级预算 + 会话迁移）：
//   - 请求体 ≥ LargeRequestBytes 视为大请求；
//   - 每个号维护滑动窗口内的**大请求计数**；计数 < 预算 → 正常承接（粘性保留、
//     前缀缓存照常命中）；
//   - 计数达到预算 → **下一条**大请求不再给它 → 换一个号 → 会话迁移过去并
//     重新绑定（迁移只发生一次，之后该对话固定在新号，继续命中新号的前缀缓存）；
//   - 滑动窗口滑过后旧号自动恢复承接资格。
//
// 与 legacy 逐请求轮换的关系：预算 <=0 时退回 legacy（excluded/cooldown 路径），
// 由调用方按配置选择。ithtelab 的默认值是「预算制开启 + legacy 关闭」。
type largeRequestGate struct {
	mu sync.Mutex
	// counts 滑动窗口内的大请求计数：uid → [(时间戳)]，旧时间戳按需清理。
	counts map[string][]time.Time
}

func newLargeRequestGate() *largeRequestGate {
	return &largeRequestGate{counts: map[string][]time.Time{}}
}

// isLarge 报告请求体是否达到大请求阈值。threshold <=0 = 功能关闭（恒 false）。
func (g *largeRequestGate) isLarge(threshold int64, bodySize int64) bool {
	if threshold <= 0 || g == nil {
		return false
	}
	return bodySize >= threshold
}

// budgetExceeded 报告该 uid 在窗口内的大请求计数是否已达预算。
// 顺带清理窗口外的旧时间戳。预算 <=0 表示「预算制关闭」（恒 false，走 legacy）。
func (g *largeRequestGate) budgetExceeded(uid string, budget int, window time.Duration) bool {
	if g == nil || budget <= 0 || window <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	kept := g.counts[uid][:0]
	for _, at := range g.counts[uid] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	g.counts[uid] = kept
	return len(kept) >= budget
}

// mark 登记一个 uid 刚承接了大请求（滑动窗口计数 +1）。
func (g *largeRequestGate) mark(uid string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.counts[uid] = append(g.counts[uid], time.Now())
	g.mu.Unlock()
}

// excluded 返回当前窗口内应排除的 uid 集合（legacy 逐请求轮换路径用，
// 预算制关闭时才有意义）。过期的顺带清理。
func (g *largeRequestGate) excluded(window time.Duration) map[string]bool {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := map[string]bool{}
	for uid, at := range g.counts {
		if len(at) > 0 && now.Sub(at[len(at)-1]) < window {
			out[uid] = true
		}
	}
	return out
}

// mergeExcluded 合并两组排除（tried 轮换已试 + largeExcluded 大请求窗口）。
func mergeExcluded(tried map[string]bool, largeExcluded map[string]bool) map[string]bool {
	if len(largeExcluded) == 0 {
		return tried
	}
	out := make(map[string]bool, len(tried)+len(largeExcluded))
	for k, v := range tried {
		out[k] = v
	}
	for k := range largeExcluded {
		out[k] = true
	}
	return out
}

// excludedBudget 返回窗口内大请求计数已达预算的 uid 集合（预算制专用）。
// 顺带清理窗口外旧时间戳。
func (g *largeRequestGate) excludedBudget(budget int, window time.Duration) map[string]bool {
	if g == nil || budget <= 0 || window <= 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := map[string]bool{}
	for uid, stamps := range g.counts {
		var kept int
		var alive []time.Time
		for _, at := range stamps {
			if now.Sub(at) < window {
				alive = append(alive, at)
				kept++
			}
		}
		g.counts[uid] = alive
		if kept >= budget {
			out[uid] = true
		}
	}
	return out
}

// acquire 原子地「检查预算并登记」：窗口内计数 < 预算 → 登记 +1 并返回 true
// （本请求可由该 uid 承接）；已超预算 → 返回 false（不登记，调用方换号）。
// 把 budgetExceeded + mark 合并为单一原子操作，消除「多并发流同时通过检查、
// 结束时各自 mark 导致计数短暂超预算」的 TOCTOU 竞态。
func (g *largeRequestGate) acquire(uid string, budget int, window time.Duration) bool {
	if g == nil || budget <= 0 || window <= 0 {
		return true // 功能关闭：无条件放行
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	kept := g.counts[uid][:0]
	for _, at := range g.counts[uid] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= budget {
		g.counts[uid] = kept
		return false
	}
	g.counts[uid] = append(kept, now)
	return true
}
