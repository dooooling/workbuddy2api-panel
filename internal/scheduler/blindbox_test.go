package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// blindboxStub 按队列依次返回 quota，记录每次 open 的 count。
type blindboxStub struct {
	quotas []string
	qi     int
	opens  []int
}

func (s *blindboxStub) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/activity/growth/buddy/quota":
			// 配额用尽后返回 affordable=0（等价于能量不足），让调用方自然收敛。
			if s.qi >= len(s.quotas) {
				w.Write([]byte(`{"code":0,"data":{"affordable":0,"balance":0}}`))
				return
			}
			w.Write([]byte(s.quotas[s.qi]))
			s.qi++
		case "/activity/growth/buddy/open":
			var body struct {
				Count int `json:"count"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.opens = append(s.opens, body.Count)
			w.Write([]byte(`{"code":0,"data":{"count":` + strconv.Itoa(body.Count) +
				`,"results":[{"template":{"name":"斜杠喵","rarity":"R"}}]}}`))
		default:
			w.Write([]byte(`{"code":0,"data":{}}`))
		}
	})
}

func blindboxScheduler(t *testing.T, s *blindboxStub) *Scheduler {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	p := pool.New("")
	p.Add(a)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up})
}

// TestOpenBlindboxesBatchesByMaxOpenCount 分批开：每批不超过上游 max_open_count，
// 开完再查配额，直到 affordable 归零。
func TestOpenBlindboxesBatchesByMaxOpenCount(t *testing.T) {
	stub := &blindboxStub{quotas: []string{
		`{"code":0,"data":{"affordable":7,"balance":70,"cost_per_open":10,"max_open_count":5}}`,
		`{"code":0,"data":{"affordable":2,"balance":20,"cost_per_open":10,"max_open_count":5}}`,
		`{"code":0,"data":{"affordable":0,"balance":0,"cost_per_open":10,"max_open_count":5}}`,
	}}
	s := blindboxScheduler(t, stub)
	s.openBlindboxes(s.cfg.Pool.AuthByUID("u1"))

	if len(stub.opens) != 2 || stub.opens[0] != 5 || stub.opens[1] != 2 {
		t.Fatalf("opens=%v want [5 2]（每批截断到 max_open_count）", stub.opens)
	}
}

// TestOpenBlindboxesSkipsWhenNoEnergy 能量不足时一个 open 都不发（绝大多数账号的常态）。
func TestOpenBlindboxesSkipsWhenNoEnergy(t *testing.T) {
	stub := &blindboxStub{quotas: []string{
		`{"code":0,"data":{"affordable":0,"balance":3,"cost_per_open":10,"max_open_count":5}}`,
	}}
	s := blindboxScheduler(t, stub)
	s.openBlindboxes(s.cfg.Pool.AuthByUID("u1"))

	if len(stub.opens) != 0 {
		t.Fatalf("不该开盒，实际 opens=%v", stub.opens)
	}
}

// TestOpenBlindboxesDerivesFromBalance 上游没给 affordable 时按 balance/单价推导。
func TestOpenBlindboxesDerivesFromBalance(t *testing.T) {
	stub := &blindboxStub{quotas: []string{
		`{"code":0,"data":{"balance":25,"cost_per_open":10,"max_open_count":5}}`,
		`{"code":0,"data":{"balance":15,"cost_per_open":10,"max_open_count":5}}`,
		`{"code":0,"data":{"balance":5,"cost_per_open":10,"max_open_count":5}}`,
	}}
	s := blindboxScheduler(t, stub)
	s.openBlindboxes(s.cfg.Pool.AuthByUID("u1"))

	// 25 → 开 2（上限 5 未触及），剩 15 → 开 1，剩 5 → 停
	if len(stub.opens) != 2 || stub.opens[0] != 2 || stub.opens[1] != 1 {
		t.Fatalf("opens=%v want [2 1]", stub.opens)
	}
}

// TestOpenBlindboxesRespectsSwitch 开关关闭时一个上游请求都不发（能量只攒不花）。
func TestOpenBlindboxesRespectsSwitch(t *testing.T) {
	stub := &blindboxStub{quotas: []string{
		`{"code":0,"data":{"affordable":5,"balance":103,"cost_per_open":10,"max_open_count":5}}`,
	}}
	s := blindboxScheduler(t, stub)
	s.SetBlindboxEnabled(false)
	s.openBlindboxes(s.cfg.Pool.AuthByUID("u1"))

	if stub.qi != 0 || len(stub.opens) != 0 {
		t.Fatalf("开关关闭后不该发请求，quota_calls=%d opens=%v", stub.qi, stub.opens)
	}

	// 重新打开后照常工作（热生效）
	s.SetBlindboxEnabled(true)
	s.openBlindboxes(s.cfg.Pool.AuthByUID("u1"))
	if len(stub.opens) == 0 {
		t.Fatal("重新开启后应能开盒")
	}
}
