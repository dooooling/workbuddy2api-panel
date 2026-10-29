// streak.go 连登管家：签到排程后自动检查连登兑换档位 → 可兑换即兑换 → 按抽奖次数抽奖。
//
// 背景（2026-09-12）：成长中心连登档位（7d/14d/28d）按连续登录天数解锁，兑换发
// credit/energy/补签卡/抽奖次数；抽奖次数只能从兑换获得。兑换按钮在 UI 上恒可点，
// 但未解锁时服务端 403「连续登录天数不足」——所以放在每日签到后跑一遍（幂等），
// 到天数那天自动完成「兑换 → 抽奖」闭环，无需人工盯。
package scheduler

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// RunStreakBonusNow 对所有可用账号执行连登兑换 + 抽奖（幂等：locked/无次数自动跳过）。
// 由签到排程（RunCheckinNow）末尾调用；也可面板手动触发。
func (s *Scheduler) RunStreakBonusNow() {
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.AccessTokenValue() == "" {
			continue
		}
		// 企业版跳过：growth 端点对企业号 403。
		// global **不再跳过**：活跃上报在国际站能点亮连登（实测 days=6），growth 端点按
		// realm 走 workbuddy.ai，档位兑换/抽奖幂等（未解锁 403 只记一行日志）。
		if a.IsEnterprise() {
			continue
		}
		s.streakBonusAccount(a)
	}
}

// streakBonusAccount 单账号：补签保连登 → 礼包/补偿 → 兑换所有已解锁档位 → 抽完所有 chances。
func (s *Scheduler) streakBonusAccount(a *auth.Auth) {
	// 0. 补签保连登：昨日漏签且有补签卡则补上（连续天数一断就要重攒 7 天）。
	s.makeupYesterday(a)
	// 0.5 礼包/补偿（每号一次，无则业务错误静默跳过）。
	if credit, err := s.cfg.Upstream.ClaimGift(a); err == nil {
		log.Printf("streak-bonus %s: 🎊 新手礼包 +%dc", logfmt.Label(a.UID, a.Nickname), credit)
	}
	// 补偿改为「先查状态 → 待领才领」（issue #189）：旧写法 `if credit, err :=
	// ClaimCompensation(...); err == nil` 把「没有补偿」「已经领过」与「领取真的
	// 失败」压成同一形态，补偿带截止日（实测那次为 10/22）时签到一旦停摆就静默
	// 过期。现在无待领不记日志，但状态查询失败与领取失败一律显式告警。
	s.claimCompensationQuiet(a)

	full, err := s.cfg.Upstream.GrowthStreakFull(a)
	if err != nil {
		log.Printf("streak-bonus %s: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	statuses := map[string]string{
		"7d":  full.RedemptionStatus.Tier7dStatus,
		"14d": full.RedemptionStatus.Tier14dStatus,
		"28d": full.RedemptionStatus.Tier28dStatus,
	}
	for _, tier := range full.RedemptionStatus.Tiers {
		status := statuses[tier.Tier]
		if status == "locked" || status == "claimed" {
			continue
		}
		if err := s.cfg.Upstream.GrowthRedeemTier(a, tier.Tier); err != nil {
			// 未解锁（403）属预期，静默；其余记日志。
			log.Printf("streak-bonus %s: redeem %s: %v", logfmt.Label(a.UID, a.Nickname), tier.Tier, err)
			continue
		}
		log.Printf("streak-bonus %s: ★ 兑换 %s 档（+%dc +%de 卡×%d 抽奖×%d）",
			a.UID, tier.Tier, tier.Credit, tier.Energy, tier.Cards, tier.Chances)
	}
	// 开盲盒：兑换刚发的能量要立刻用掉（每次 10，单次上限由上游 max_open_count 给）。
	s.openBlindboxes(a)
	// 抽奖：按当前 chances 全抽完（兑换刚发的次数已在服务端累加）。
	chances, err := s.cfg.Upstream.LotteryChances(a)
	if err != nil {
		log.Printf("streak-bonus %s: lottery summary: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	for i := 0; i < chances; i++ {
		raw, err := s.cfg.Upstream.LotteryDraw(a)
		if err != nil {
			// 「lottery disabled」是上游把抽奖整体关了的活动状态（2026-10 实测），
			// 不是故障：兑换拿到的次数会留在账号上，等上游重开再抽。按业务状态
			// 静默收口，否则每轮签到都为每个号刷一条错误。
			if isLotteryDisabled(err) {
				if i == 0 {
					log.Printf("streak-bonus %s: 抽奖暂未开放（上游 lottery disabled），已保留 %d 次机会",
						logfmt.Label(a.UID, a.Nickname), chances)
				}
				return
			}
			log.Printf("streak-bonus %s: draw: %v", logfmt.Label(a.UID, a.Nickname), err)
			return
		}
		log.Printf("streak-bonus %s: 🎲 第%d抽 %s", logfmt.Label(a.UID, a.Nickname), i+1, compactJSON(raw))
	}
	if chances > 0 {
		log.Printf("streak-bonus %s: 抽奖完成 %d 次", logfmt.Label(a.UID, a.Nickname), chances)
	}
}

// isLotteryDisabled 判断抽奖是否被上游整体关闭（code=400 / msg "lottery disabled"）。
func isLotteryDisabled(err error) bool {
	var ue *upstream.Error
	if errors.As(err, &ue) {
		return strings.Contains(strings.ToLower(ue.Msg), "lottery disabled")
	}
	return false
}

// compactJSON 裁剪奖品载荷（日志单行可读）。
func compactJSON(raw json.RawMessage) string {
	s := string(raw)
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}

// makeupYesterday 昨日漏签且有补签卡时自动补签（保住连登连续天数）。
// 无卡 / 无漏签 / 查询失败均静默（不影响主流程）。
func (s *Scheduler) makeupYesterday(a *auth.Auth) {
	missed, err := s.cfg.Upstream.HeatmapYesterdayMissed(a)
	if err != nil || !missed {
		return
	}
	full, err := s.cfg.Upstream.GrowthStreakFull(a)
	if err != nil || full.MakeupCards.Balance <= 0 {
		return
	}
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := s.cfg.Upstream.UseMakeupCard(a, yesterday); err != nil {
		log.Printf("streak-bonus %s: 补签 %s 失败: %v", logfmt.Label(a.UID, a.Nickname), yesterday, err)
		return
	}
	log.Printf("streak-bonus %s: ★ 已用补签卡补签 %s（保连登）", logfmt.Label(a.UID, a.Nickname), yesterday)
}

// ---------------------------------------------------------------------------
// 开盲盒
// ---------------------------------------------------------------------------

const (
	// blindboxMaxRounds 单轮最多开几批（每批 ≤ 上游 max_open_count）。兜底防上游
	// affordable 恒正时死循环；实测单号能量远小于 5×5×10。
	blindboxMaxRounds = 8
	// blindboxDefaultCost 上游没给 cost_per_open 时的兜底单价（实测 10）。
	blindboxDefaultCost = 10
	// blindboxDefaultMax 上游没给 max_open_count 时的兜底单批上限（实测 5）。
	blindboxDefaultMax = 5
)

// openBlindboxes 用能量开盲盒：查配额 → 按 affordable 分批开 → 直到能量不够。
//
// 语义：
//   - 能量不足（affordable<=0 且余额 < 单价）直接静默返回：绝大多数账号常态如此；
//   - 每批数量取 min(affordable, max_open_count)，开完再查一次配额，直到开不动；
//   - 业务错误只记一行（不重试），避免上游异常时每轮刷屏。
//
// 2026-10-29 实测：GET energy/quota + POST buddy/open 三端点在本网关出站口径下均可用，
// 单次 10 能量，单批上限 5，返回 results[].template.name（R/SR/UR 伙伴）。
func (s *Scheduler) openBlindboxes(a *auth.Auth) {
	total := 0
	var names []string
	for round := 0; round < blindboxMaxRounds; round++ {
		q, err := s.cfg.Upstream.BlindboxQuotaNow(a)
		if err != nil {
			// 配额接口失败不影响主流程（有些账号没有伙伴体系）；首轮才记一行。
			if round == 0 {
				log.Printf("streak-bonus %s: 盲盒配额: %v", logfmt.Label(a.UID, a.Nickname), err)
			}
			return
		}
		cost := q.CostPerOpen
		if cost <= 0 {
			cost = blindboxDefaultCost
		}
		n := q.Affordable
		if n <= 0 {
			// 上游没算 affordable 时按能量本地推导。
			n = q.Balance / cost
		}
		if n <= 0 {
			break
		}
		perCall := q.MaxOpenCount
		if perCall <= 0 {
			perCall = blindboxDefaultMax
		}
		if n > perCall {
			n = perCall
		}
		res, err := s.cfg.Upstream.OpenBlindbox(a, n)
		if err != nil {
			if total == 0 {
				log.Printf("streak-bonus %s: 开盲盒: %v", logfmt.Label(a.UID, a.Nickname), err)
			}
			return
		}
		total += res.Count
		for _, p := range res.Results {
			if p.Template.Name != "" {
				names = append(names, p.Template.Name)
			}
		}
		if res.Count <= 0 {
			break
		}
	}
	if total > 0 {
		shown := names
		if len(shown) > 5 {
			shown = shown[:5]
		}
		suffix := ""
		if len(names) > len(shown) {
			suffix = "…"
		}
		log.Printf("streak-bonus %s: 🎁 开盲盒 ×%d：%s%s",
			logfmt.Label(a.UID, a.Nickname), total, strings.Join(shown, "、"), suffix)
	}
}
