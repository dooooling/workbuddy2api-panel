package server

import (
	"sync"
	"time"
)

// largeRequestGate 大上下文请求的账号排除窗口（ithtelab #387 现场复刻）：
// 单号在窗口内被连续压超大上下文（DSH 长上下文请求 2.4~6MB）会触发上游
// 11140 "unusual activity" 风控，把整批号封掉。本结构记录「窗口内已承接
// 大请求的 uid」，选号时排除它们，迫使大请求逐请求轮换。
//
// 语义（对齐 ithtelab #362 补丁 0043）：
//   - 请求体 ≥ LargeRequestBytes 视为大请求；
//   - 选号时排除窗口内已承接大请求的号；全部号都在窗口内则放行（退化普通
//     轮换，避免 503）——排除是尽力而为，不是硬阻断；
//   - 窗口过期（LargeRequestCooldown）自动解除；
//   - 粘性会话不绑定大请求（放弃粘性，成功也不写绑定）。
type largeRequestGate struct {
	mu      sync.Mutex
	entries map[string]time.Time // uid → 上次承接大请求的时刻
}

func newLargeRequestGate() *largeRequestGate {
	return &largeRequestGate{entries: map[string]time.Time{}}
}

// isLarge 报告请求体是否达到大请求阈值。threshold <=0 = 功能关闭（恒 false）。
func (g *largeRequestGate) isLarge(threshold int64, bodySize int64) bool {
	if threshold <= 0 || g == nil {
		return false
	}
	return bodySize >= threshold
}

// excluded 返回当前窗口内应排除的 uid 集合（过期的顺带清理）。
// 全部健康号都在窗口内时调用方应放行（返回值仅作参考，不硬阻断）。
func (g *largeRequestGate) excluded(window time.Duration) map[string]bool {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := map[string]bool{}
	for uid, at := range g.entries {
		if now.Sub(at) < window {
			out[uid] = true
		} else {
			delete(g.entries, uid)
		}
	}
	return out
}

// mark 登记一个 uid 刚承接了大请求。
func (g *largeRequestGate) mark(uid string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.entries[uid] = time.Now()
	g.mu.Unlock()
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
