// growthstatus.go 面板「连登与能量」只读快照。
//
// 背景：连登天数、档位兑换状态、补签卡、盲盒能量这些只有成长中心网页看得到，
// 网关内部其实一直在读写（连登管家 / 盲盒），但面板没有出口——排查「为什么只有 1 天」
// 只能翻日志或开网页对照。本文件把 GrowthStreakFull + 能量 + 伙伴聚合成一张表。
//
// 设计要点：
//   - **只读**：不发任何写请求（兑换/开盒仍由连登管家在签到末尾做）；
//   - **缓存 60s**：面板首页每 5 秒轮询 overview，若每次都打上游，5 个账号 × 3 个端点
//     会把上游打满；?refresh=1 可强制刷新（面板上的「刷新」按钮）；
//   - 企业版跳过（growth 端点 403）；单账号出错只在该行标 error，不影响其他行。
package panel

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
)

// growthStatusTTL 快照缓存时长：与首页轮询（5s）解耦，避免把上游打满。
const growthStatusTTL = 60 * time.Second

// growthStatusItem 单账号的连登/能量快照。
type growthStatusItem struct {
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	Realm      string `json:"realm"`
	Days       int    `json:"days"`
	MonthTotal int    `json:"month_total"`
	NextTier   string `json:"next_tier,omitempty"`
	NextRemain int    `json:"next_tier_remaining,omitempty"`
	Tier7d     string `json:"tier_7d,omitempty"`
	Tier14d    string `json:"tier_14d,omitempty"`
	Tier28d    string `json:"tier_28d,omitempty"`
	Cards      int    `json:"makeup_cards"`
	CardsMax   int    `json:"makeup_cards_max"`
	Energy     int    `json:"energy"`
	Buddy      string `json:"buddy,omitempty"`
	Error      string `json:"error,omitempty"`
}

// growthStatusCache 快照缓存（含生成时间）。
type growthStatusCache struct {
	mu    sync.Mutex
	at    time.Time
	items []growthStatusItem
}

// growthStatus GET /panel/api/growth_status[?refresh=1]
func (p *Panel) growthStatus(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	items, at := p.growthSnapshot(force)
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"updated_at": at.Unix(),
		"stale":      time.Since(at) > growthStatusTTL,
	})
}

// growthSnapshot 返回快照（命中缓存则直接返回）。force=true 忽略缓存。
func (p *Panel) growthSnapshot(force bool) ([]growthStatusItem, time.Time) {
	p.growthCache.mu.Lock()
	defer p.growthCache.mu.Unlock()
	if !force && len(p.growthCache.items) > 0 && time.Since(p.growthCache.at) < growthStatusTTL {
		return p.growthCache.items, p.growthCache.at
	}
	items := p.collectGrowthStatus()
	p.growthCache.items = items
	p.growthCache.at = time.Now()
	return items, p.growthCache.at
}

// collectGrowthStatus 逐账号拉连登 + 能量 + 伙伴（串行；账号数个位数，代价可接受）。
func (p *Panel) collectGrowthStatus() []growthStatusItem {
	states := p.cfg.Pool.List()
	out := make([]growthStatusItem, 0, len(states))
	for _, st := range states {
		it := growthStatusItem{UID: st.UID, Nickname: st.Nickname}
		a := p.cfg.Pool.AuthByUID(st.UID)
		if a == nil {
			continue
		}
		it.Realm = a.Realm()
		if a.IsEnterprise() {
			it.Error = "企业版无个人成长体系"
			out = append(out, it)
			continue
		}
		full, err := p.cfg.Upstream.GrowthStreakFull(a)
		if err != nil {
			it.Error = err.Error()
			out = append(out, it)
			log.Printf("panel: growth-status %s: %v", logfmt.Label(st.UID, st.Nickname), err)
			continue
		}
		it.Days = full.Streak.Days
		it.MonthTotal = full.Streak.MonthTotalDays
		it.NextTier = full.Streak.NextTier
		it.NextRemain = full.Streak.NextTierRemaining
		it.Tier7d = full.RedemptionStatus.Tier7dStatus
		it.Tier14d = full.RedemptionStatus.Tier14dStatus
		it.Tier28d = full.RedemptionStatus.Tier28dStatus
		it.Cards = full.MakeupCards.Balance
		it.CardsMax = full.MakeupCards.Max
		// 能量/伙伴是附加信息：失败不整行报错（部分账号没有伙伴体系）。
		if en, eerr := p.cfg.Upstream.BlindboxEnergyNow(a); eerr == nil {
			it.Energy = en.Balance
		}
		if buddy, berr := p.cfg.Upstream.BuddyInfo(a); berr == nil && buddy != nil {
			it.Buddy = buddy.Name
		}
		out = append(out, it)
	}
	return out
}
