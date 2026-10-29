package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestBlindboxEndpoints 盲盒三端点：路径、请求体与响应解析（形状按 2026-10-29 真机实测）。
func TestBlindboxEndpoints(t *testing.T) {
	var gotPath string
	var gotCount float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		switch r.URL.Path {
		case "/activity/growth/energy":
			w.Write([]byte(`{"code":0,"data":{"balance":103,"total_consumed":0,"total_earned":103}}`))
		case "/activity/growth/buddy/quota":
			w.Write([]byte(`{"code":0,"data":{"affordable":5,"balance":103,"cost_per_open":10,"max_open_count":5}}`))
		case "/activity/growth/buddy/open":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			gotCount, _ = body["count"].(float64)
			w.Write([]byte(`{"code":0,"data":{"count":5,"results":[
				{"instance":{"id":27097513},"template":{"id":16,"name":"斜杠喵","rarity":"R"},"is_first":false},
				{"instance":{"id":27097514},"template":{"id":17,"name":"甜心喵","rarity":"R"},"is_first":false}
			]}}`))
		default:
			w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u1"}

	en, err := c.BlindboxEnergyNow(a)
	if err != nil || gotPath != "/activity/growth/energy" || en.Balance != 103 || en.TotalConsumed != 0 {
		t.Fatalf("energy=%+v err=%v path=%s", en, err, gotPath)
	}
	q, err := c.BlindboxQuotaNow(a)
	if err != nil || q.Affordable != 5 || q.CostPerOpen != 10 || q.MaxOpenCount != 5 {
		t.Fatalf("quota=%+v err=%v", q, err)
	}
	res, err := c.OpenBlindbox(a, 5)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if gotPath != "/activity/growth/buddy/open" || gotCount != 5 {
		t.Fatalf("open path=%s count=%v", gotPath, gotCount)
	}
	if res.Count != 5 || len(res.Results) != 2 || res.Results[0].Template.Name != "斜杠喵" {
		t.Fatalf("result=%+v", res)
	}
}
