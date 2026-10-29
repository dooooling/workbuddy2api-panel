// travel.go 猫猫旅行巡检状态机：随旅行时点（travel_hours，默认 09 点）对池内每个可用账号单趟推进一次。
// 无猫 → 同意协议 + 领养；有猫 → 按 travel/status 分派 派出 / 领奖 / 跳过。
package scheduler

import (
	"fmt"
	"log"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/logfmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

const (
	// travelLocationID 派出地点固定 4（古镇客栈）：4 个地点收益/时长区间完全相同，无最优解。
	travelLocationID = 4

	// travelStateIdle 空闲可派出；travelStateTraveling 在途；travelStateArrived 到站可领奖。
	travelStateIdle      = "idle"
	travelStateTraveling = "traveling"
	travelStateArrived   = "arrived"
)

// travelAccountDelay 账号间限速：全量账号约 40s，避免上游风控。测试可置 0。
var travelAccountDelay = 800 * time.Millisecond

// activityAccountDelay 活跃上报账号间限速：与旅行同口径，避免上游风控。测试可置 0。
var activityAccountDelay = 800 * time.Millisecond

// adoptReportGap 领养前置上报后的等待：给上游事件处理留时间再发 buddy/first。
// 对齐 scripts/task_first_buddy.py 实测的 1.05s 间隔口径。测试可置 0。
var adoptReportGap = 1050 * time.Millisecond

// cstZone 上游每日重置按自然日 00:00 CST（Asia/Shanghai）。中国无夏令时，固定 +8 即可，
// 不依赖容器 tzdata。
var cstZone = time.FixedZone("CST", 8*60*60)

// travelDay 返回 t 所属的上游自然日（CST），格式 2006-01-02。
func travelDay(t time.Time) string {
	return t.In(cstZone).Format("2006-01-02")
}

// RunTravelNow 立即对池内所有可用账号执行一趟旅行巡检。
// 禁用账号跳过；暂停选号（paused）账号照常参与——旅行是纯 RPC（状态/派出/
// 领奖 + 领养前置上报），不发模型对话，与「让位防风控」不冲突；
// 401/查询失败只跳过该账号本轮（不强刷 token，交 22:00 keepalive）；
// 账号间限速 travelAccountDelay。
func (s *Scheduler) RunTravelNow() {
	first := true
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			continue
		}
		if a.IsEnterprise() {
			continue // 企业版门控：无个人成长体系（growth 端点企业号 403），不发起任何上游调用
		}
		if a.IsGlobal() {
			// 国际版：**只做伙伴领养尝试**，不做出行/领奖。
			// 2026-10-29 实测（workbuddy.ai）：buddy/info 与 agreement 可用、travel/status
			// 返回空对象（无旅行体系），而 buddy/first 有前置条件「至少一次对话」——
			// 该口径不认网关的代理对话，只能等官方客户端聊过一次后自然满足。
			// 因此每天试一次（adoptTried 幂等），条件满足即自动领养，否则静默重试。
			if !first {
				time.Sleep(travelAccountDelay)
			}
			first = false
			s.adoptGlobalIfNeeded(a)
			continue
		}
		if !first {
			time.Sleep(travelAccountDelay)
		}
		first = false
		s.travelOne(a)
	}
}

// travelOne 单账号单趟状态机：查有无猫 + 查状态 + 最多一个动作，不轮询不等待。
func (s *Scheduler) travelOne(a *auth.Auth) {
	buddy, err := s.cfg.Upstream.BuddyInfo(a)
	if err != nil {
		log.Printf("travel %s: buddy-info: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	if buddy == nil {
		s.travelAdopt(a)
		return
	}
	ts, err := s.cfg.Upstream.TravelStatus(a)
	if err != nil {
		log.Printf("travel %s: status: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	switch ts.State {
	case travelStateArrived:
		s.travelClaim(a, ts)
	case travelStateIdle:
		s.travelDepart(a, ts)
	case travelStateTraveling:
		log.Printf("travel %s: skip (traveling record=%d)", logfmt.Label(a.UID, a.Nickname), ts.RecordID)
	default:
		log.Printf("travel %s: skip (unknown state %q)", logfmt.Label(a.UID, a.Nickname), ts.State)
	}
}

// travelDepart 空闲且未达当日上限时派出（每日 1 次，自然日 00:00 CST 重置）。
func (s *Scheduler) travelDepart(a *auth.Auth, ts *upstream.TravelState) {
	if ts.DailyLimitReached {
		log.Printf("travel %s: skip (daily limit reached)", logfmt.Label(a.UID, a.Nickname))
		return
	}
	if err := s.cfg.Upstream.TravelDepart(a, travelLocationID); err != nil {
		log.Printf("travel %s: depart: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	log.Printf("travel %s: depart ok location=%d", logfmt.Label(a.UID, a.Nickname), travelLocationID)
}

// travelClaim 到站领奖（必须带 record_id）。
func (s *Scheduler) travelClaim(a *auth.Auth, ts *upstream.TravelState) {
	if ts.RecordID == 0 {
		log.Printf("travel %s: claim skipped (arrived but no record_id)", logfmt.Label(a.UID, a.Nickname))
		return
	}
	reward, err := s.cfg.Upstream.TravelClaim(a, ts.RecordID)
	if err != nil {
		log.Printf("travel %s: claim record=%d: %v", logfmt.Label(a.UID, a.Nickname), ts.RecordID, err)
		return
	}
	log.Printf("travel %s: claim ok record=%d reward=%d", logfmt.Label(a.UID, a.Nickname), ts.RecordID, reward)
}

// travelAdopt 无猫时领养，链路：report → agreement → buddy/first。
//
// report 必须先跑（scripts/task_first_buddy.py 实测）：一条 chat_request_send 上报
// 点亮 growth 连登并**解锁 first_buddy 任务**；未上报时 buddy/first 会返回
// 400 "first_buddy task not completed yet"——该门槛的真实来源是"当日无活跃上报"，
// 不是账号问题（report.go 注释亦明确「解锁 first_buddy 任务（领养前置）」）。
// conversation 门槛未达标仍属预期行为，记一次当日已试后静默跳过，不再重试。
func (s *Scheduler) travelAdopt(a *auth.Auth) {
	if s.adoptTriedToday(a.UID) {
		return
	}
	// 前置：解锁 first_buddy 任务（幂等；失败不阻塞，让 buddy/first 按既有错误路径暴露）。
	if err := s.cfg.Upstream.ReportChatActivity(a, fmt.Sprintf("wb2api-adopt-%d", time.Now().UnixMilli()), ""); err != nil {
		log.Printf("travel %s: adopt preflight report: %v", logfmt.Label(a.UID, a.Nickname), err)
	} else {
		time.Sleep(adoptReportGap) // 给上游事件处理留时间（对齐脚本实测的 1.05s 间隔口径）
	}
	if err := s.cfg.Upstream.BuddyAgreement(a); err != nil {
		log.Printf("travel %s: agreement: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	err := s.cfg.Upstream.BuddyFirst(a)
	switch {
	case err == nil:
		log.Printf("travel %s: adopt ok (realm=%s)", logfmt.Label(a.UID, a.Nickname), a.Realm())
	case upstream.IsBuddyTaskIncomplete(err):
		s.markAdoptTried(a.UID)
		log.Printf("travel %s: adopt skipped (conversation threshold not reached, retry tomorrow)", logfmt.Label(a.UID, a.Nickname))
	default:
		log.Printf("travel %s: adopt: %v", logfmt.Label(a.UID, a.Nickname), err)
	}
}

// adoptTriedToday 该账号当日是否已判定领养门槛未达。
func (s *Scheduler) adoptTriedToday(uid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptTried[uid] == travelDay(time.Now())
}

// markAdoptTried 记录该账号当日已尝试领养且未过门槛。
func (s *Scheduler) markAdoptTried(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adoptTried[uid] = travelDay(time.Now())
}

// adoptGlobalIfNeeded 国际版账号的伙伴领养尝试：已有伙伴则静默返回，否则走与国内
// 同一条 travelAdopt（前置上报 → 同意协议 → buddy/first）。
//
// 为什么单独一个入口：国际版没有旅行体系（travel/status 返回空对象），只有伙伴；
// 而 buddy/first 的前置「至少一次对话」由上游判定，网关推不动——所以这里只负责
// 「每天试一次」，条件满足那天自动领养，未满足由 travelAdopt 记一行并在当日收敛。
func (s *Scheduler) adoptGlobalIfNeeded(a *auth.Auth) {
	buddy, err := s.cfg.Upstream.BuddyInfo(a)
	if err != nil {
		log.Printf("travel %s: buddy-info: %v", logfmt.Label(a.UID, a.Nickname), err)
		return
	}
	if buddy != nil {
		return // 已领养：国际版无旅行体系，无需后续动作
	}
	s.travelAdopt(a)
}
