package scheduler

import (
	"hash/fnv"
	"time"
)

// humanize.go 人味化抖动（misakano #84 风控复盘的反制）：
// 上游风控最容易捞的特征是「同出口 IP 上全部账号以完全一致的机械节拍活动」——
// 余额刷新精确每 5 分钟整点全账号并发齐射、补领清扫精确每 15 分钟整点。
// 本文件提供**稳定抖动**：对 (任务名 + uid) 做 FNV 哈希派生偏移，同账号每轮偏移
// 恒定（可复现、可测试），不同账号互相错开；进程重启换种子则整体换相位。
//
// 全部缺省关闭（permill=0 → 原样返回），旧行为逐字不变；通过 config 开启。

// jitterPermille 千分比抖动：把 base 在 ±(permill/1000)×base 内偏移。
// permill<=0 或 base<=0 时原样返回（功能关闭）。
func jitterPermille(base time.Duration, permill int, key string) time.Duration {
	if permill <= 0 || base <= 0 {
		return base
	}
	span := int64(base) * int64(permill) / 1000
	if span <= 0 {
		return base
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	// [−span/2, +span/2]
	off := int64(h.Sum32()%(uint32(span)+1)) - span/2
	out := base + time.Duration(off)
	if out < 0 {
		out = 0
	}
	return out
}

// spreadInterval 账号摊开：全账号串行遍历时给第 i 个账号加 [0, window) 的
// 独立延迟（消除「同一秒全池同动作」）。window<=0 或 i<0 原样返回 0。
func spreadInterval(window time.Duration, permill int, key string) time.Duration {
	if window <= 0 || permill <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum32() % uint32(window))
}
