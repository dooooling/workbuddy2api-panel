package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestMarketExpertSearchSendsKeywordAndLanguage 「体验某位专家」类任务只给名字时靠搜索认人：
// 官方专家页的搜索框发的仍是列表接口，多带 keyword 与 X-Language=zh-CN（5.9.3 逆向）。
// 同时钉住兼容解析：官方 5.9.3 起响应先取 data.items，老版本只有 data.experts。
func TestMarketExpertSearchSendsKeywordAndLanguage(t *testing.T) {
	var gotPath, gotLang string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotLang = r.Header.Get("X-Language")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		// items 形状（新版本）
		w.Write([]byte(`{"code":0,"data":{"items":[{"expert_id":"ex_1","expert_type":"agent","display_name_zh":"无畏契约拼豆专家","profession_zh":"拼豆"}]}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	experts, err := c.MarketExpertSearch(&auth.Auth{AccessToken: "at", UID: "u1"}, "agent", "无畏契约拼豆专家")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotPath != "/portal/operation-platform/market/expert/list" {
		t.Errorf("path=%q", gotPath)
	}
	if gotLang != "zh-CN" {
		t.Errorf("X-Language=%q want zh-CN", gotLang)
	}
	if gotBody["keyword"] != "无畏契约拼豆专家" {
		t.Errorf("keyword=%v", gotBody["keyword"])
	}
	if gotBody["expert_type"] != "agent" {
		t.Errorf("expert_type=%v", gotBody["expert_type"])
	}
	if gotBody["page_size"] != float64(MarketExpertPageSize) {
		t.Errorf("page_size=%v want %d", gotBody["page_size"], MarketExpertPageSize)
	}
	if len(experts) != 1 || experts[0].ExpertID != "ex_1" || experts[0].DisplayNameZH != "无畏契约拼豆专家" {
		t.Fatalf("experts=%+v", experts)
	}
}

// TestMarketExpertListBackCompat 老版本响应只有 data.experts；分页参数按页透传。
func TestMarketExpertListBackCompat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"code":0,"data":{"experts":[{"expert_id":"ex_old","display_name_zh":"老版专家"}]}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	experts, err := c.MarketExpertListPage(&auth.Auth{AccessToken: "at"}, "agent", 3)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if gotBody["page"] != float64(3) {
		t.Errorf("page=%v want 3", gotBody["page"])
	}
	if _, ok := gotBody["keyword"]; ok {
		t.Errorf("列表请求不该带 keyword: %v", gotBody)
	}
	if len(experts) != 1 || experts[0].ExpertID != "ex_old" {
		t.Fatalf("experts=%+v", experts)
	}
}
