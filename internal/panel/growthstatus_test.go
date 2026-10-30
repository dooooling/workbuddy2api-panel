package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// growthStatusStub 模拟连登/能量/伙伴三个端点，并统计 streak 调用次数（验证缓存）。
type growthStatusStub struct {
	streakCalls atomic.Int32
	buddyNull   bool
}

func (s *growthStatusStub) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/activity/growth/streak":
			s.streakCalls.Add(1)
			w.Write([]byte(`{"code":0,"data":{"streak":{"days":8,"month_total_days":16,"next_tier":"14d","next_tier_remaining":6},` +
				`"makeup_cards":{"balance":1,"max":4},` +
				`"redemption_status":{"tier_7d_status":"claimed","tier_14d_status":"locked","tier_28d_status":"locked"}}}`))
		case "/activity/growth/energy":
			w.Write([]byte(`{"code":0,"data":{"balance":103,"total_consumed":10,"total_earned":113}}`))
		case "/activity/growth/buddy/info":
			if s.buddyNull {
				w.Write([]byte(`{"code":0,"data":{"buddy":null}}`))
				return
			}
			w.Write([]byte(`{"code":0,"data":{"buddy":{"id":1,"name":"甜心喵","rarity":"R"}}}`))
		default:
			w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
}

func growthStatusPanel(t *testing.T, srv *httptest.Server) *Panel {
	t.Helper()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", Nickname: "n1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	return &Panel{cfg: Config{Pool: p, Upstream: up}}
}

// TestGrowthStatusAggregates 快照聚合：连登/档位/补签卡/能量/伙伴都从上游读出来。
func TestGrowthStatusAggregates(t *testing.T) {
	stub := &growthStatusStub{}
	srv := stub.server()
	defer srv.Close()
	p := growthStatusPanel(t, srv)

	rec := httptest.NewRecorder()
	p.growthStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/growth_status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var got struct {
		Items []growthStatusItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items=%d want 1", len(got.Items))
	}
	it := got.Items[0]
	if it.Days != 8 || it.MonthTotal != 16 || it.NextTier != "14d" || it.NextRemain != 6 {
		t.Errorf("streak 字段不对: %+v", it)
	}
	if it.Tier7d != "claimed" || it.Tier14d != "locked" || it.Tier28d != "locked" {
		t.Errorf("档位状态不对: %+v", it)
	}
	if it.Cards != 1 || it.CardsMax != 4 || it.Energy != 103 || it.Buddy != "甜心喵" {
		t.Errorf("补签卡/能量/伙伴不对: %+v", it)
	}
	if it.Error != "" {
		t.Errorf("不该有 error: %s", it.Error)
	}
}

// TestGrowthStatusCached 60s 缓存：不强制刷新时不再打上游；refresh=1 强制重取。
func TestGrowthStatusCached(t *testing.T) {
	stub := &growthStatusStub{}
	srv := stub.server()
	defer srv.Close()
	p := growthStatusPanel(t, srv)

	p.growthStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panel/api/growth_status", nil))
	if n := stub.streakCalls.Load(); n != 1 {
		t.Fatalf("首次快照 streak 调用=%d want 1", n)
	}
	p.growthStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panel/api/growth_status", nil))
	if n := stub.streakCalls.Load(); n != 1 {
		t.Errorf("命中缓存不该再打上游，streak 调用=%d want 1", n)
	}
	p.growthStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panel/api/growth_status?refresh=1", nil))
	if n := stub.streakCalls.Load(); n != 2 {
		t.Errorf("refresh=1 应强制重取，streak 调用=%d want 2", n)
	}
}

// TestGrowthStatusBuddyNull 未领养时伙伴留空（不是报错），能量照常展示。
func TestGrowthStatusBuddyNull(t *testing.T) {
	stub := &growthStatusStub{buddyNull: true}
	srv := stub.server()
	defer srv.Close()
	p := growthStatusPanel(t, srv)

	items, _ := p.growthSnapshot(true)
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].Buddy != "" {
		t.Errorf("未领养时 buddy 应为空，实际 %q", items[0].Buddy)
	}
	if items[0].Energy != 103 {
		t.Errorf("能量应照常展示，实际 %d", items[0].Energy)
	}
}
