package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestTaskWatchRecordsOnlyUnknownUnclaimed 未适配任务只记「白名单之外 + 未领 + 未锁定」：
// 白名单内的已完成任务、上游还没开放的 locked 任务都不该进清单（否则提醒全是噪音）。
func TestTaskWatchRecordsOnlyUnknownUnclaimed(t *testing.T) {
	p := &Panel{cfg: Config{StateFile: filepath.Join(t.TempDir(), "state.json")}}
	fresh := p.watchState().note("u1", []upstream.Task{
		{TaskCode: "chat_5", Target: 5, Current: 5, Claimed: true},           // 白名单内
		{TaskCode: "brand_new", Title: "新任务", Target: 1, Current: 0},         // 未知、未达标
		{TaskCode: "another", Title: "另一个", Target: 2, Current: 2},           // 未知、达标未领
		{TaskCode: "locked_one", Title: "没开放", Target: 1, Locked: true},      // locked 不记
		{TaskCode: "wb_wechat_oa_subscribe_task", Title: "关注公众号", Target: 1}, // 需人工
	})
	if len(fresh) != 3 {
		t.Fatalf("首次发现数=%d want 3 (%v)", len(fresh), fresh)
	}

	items := p.watchState().snapshot()
	if len(items) != 3 {
		t.Fatalf("清单条目=%d want 3", len(items))
	}
	// 排序：达标未领排最前
	if items[0].Code != "another" || !items[0].Claimable {
		t.Errorf("首条应为达标未领的 another，实际 %+v", items[0])
	}
	byCode := map[string]unknownTask{}
	for _, it := range items {
		byCode[it.Code] = it
	}
	if !byCode["wb_wechat_oa_subscribe_task"].Manual {
		t.Error("微信订阅任务应标为需人工")
	}
	if byCode["brand_new"].Manual {
		t.Error("普通新任务不该标需人工")
	}
	if byCode["brand_new"].FirstSeen == 0 || byCode["brand_new"].LastSeen == 0 {
		t.Error("首次/最近发现时间应被记录")
	}
}

// TestTaskWatchPersistsAndDedupes 去重靠 task_code、清单落盘（重启后仍在），
// 重复发现不刷 first_seen，只更新 last_seen 与进度。
func TestTaskWatchPersistsAndDedupes(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	p := &Panel{cfg: Config{StateFile: stateFile}}
	p.watchState().note("u1", []upstream.Task{{TaskCode: "new_a", Title: "A", Target: 2, Current: 0}})
	first := p.watchState().snapshot()[0].FirstSeen

	fresh := p.watchState().note("u2", []upstream.Task{{TaskCode: "new_a", Title: "A", Target: 2, Current: 2}})
	if len(fresh) != 0 {
		t.Errorf("重复发现不该再报首次发现: %v", fresh)
	}

	path := filepath.Join(dir, "unknown_tasks.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("清单未落盘: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("清单文件为空")
	}

	// 模拟重启：新 Panel 读同一目录，状态仍在且保留达标标记。
	p2 := &Panel{cfg: Config{StateFile: stateFile}}
	items := p2.watchState().snapshot()
	if len(items) != 1 {
		t.Fatalf("重启后条目=%d want 1", len(items))
	}
	it := items[0]
	if it.Code != "new_a" || !it.Claimable || it.MaxCurrent != 2 {
		t.Errorf("重启后状态=%+v", it)
	}
	if it.FirstSeen != first {
		t.Errorf("first_seen=%d want %d（不该被更新）", it.FirstSeen, first)
	}
	if len(it.UIDs) != 2 {
		t.Errorf("涉及账号=%v want 2 个", it.UIDs)
	}
}

// TestTaskWatchIgnore 忽略后不计入提醒统计，但仍留在清单里（可再取消）。
func TestTaskWatchIgnore(t *testing.T) {
	p := &Panel{cfg: Config{StateFile: filepath.Join(t.TempDir(), "state.json")}}
	p.watchState().note("u1", []upstream.Task{{TaskCode: "new_a", Target: 1, Current: 0}})
	if !p.watchState().setIgnored("new_a", true) {
		t.Fatal("setIgnored 应成功")
	}
	items := p.watchState().snapshot()
	if len(items) != 1 || !items[0].Ignored {
		t.Fatalf("忽略标记丢失: %+v", items)
	}
	if p.watchState().setIgnored("missing", true) {
		t.Error("未知 code 应返回 false")
	}
}

// TestAutoActionsCoveredExpertTasks 「体验某位专家」五个任务必须有自动动作；
// 需人工的任务（微信订阅）必须**不在**白名单里，否则会被当成"可以做"白跑。
func TestAutoActionsCoveredExpertTasks(t *testing.T) {
	want := []string{
		"WorkBuddy_Valorant",
		"expert_use_maidehao",
		"expert_use_douyinyin",
		"expert_use_shangjia_xuanchuan",
		"expert_use_bowangwang",
	}
	for _, code := range want {
		if autoActionFor(code) == nil {
			t.Errorf("任务 %s 缺少自动动作", code)
		}
	}
	for code := range manualTaskCodes {
		if autoActionFor(code) != nil {
			t.Errorf("需人工任务 %s 不该有自动动作", code)
		}
	}
}

// TestExpertNamed 按名字认专家：显示名或职业名全等才算命中，不做模糊匹配
// （宁可不做，也不能召唤错人白耗一次真实对话）。
func TestExpertNamed(t *testing.T) {
	experts := []upstream.MarketExpert{
		{ExpertID: "a", DisplayNameZH: "抖音策略师"},
		{ExpertID: "b", ProfessionZH: "直播电商教练"},
	}
	if e, ok := expertNamed(experts, "抖音策略师"); !ok || e.ExpertID != "a" {
		t.Errorf("display_name 匹配失败: %+v %v", e, ok)
	}
	if e, ok := expertNamed(experts, "直播电商教练"); !ok || e.ExpertID != "b" {
		t.Errorf("profession 匹配失败: %+v %v", e, ok)
	}
	if _, ok := expertNamed(experts, "抖音"); ok {
		t.Error("模糊名字不应命中")
	}
}

// TestTasksUnknownHandler 接口口径：claimable 只统计「达标未领且非人工/未忽略」，
// 前端黄条与任务中心卡片都按这三个计数显示，算错会直接误导优先级。
func TestTasksUnknownHandler(t *testing.T) {
	p := &Panel{cfg: Config{StateFile: filepath.Join(t.TempDir(), "state.json")}}
	p.watchState().note("u1", []upstream.Task{
		{TaskCode: "done_unclaimed", Title: "达标未领", Target: 1, Current: 1},
		{TaskCode: "in_progress", Title: "进行中", Target: 3, Current: 1},
		{TaskCode: "wb_wechat_oa_subscribe_task", Title: "微信", Target: 1},
	})
	p.watchState().setIgnored("in_progress", true)

	rec := httptest.NewRecorder()
	p.tasksUnknown(rec, httptest.NewRequest(http.MethodGet, "/panel/api/tasks/unknown", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var got struct {
		Items     []unknownTask `json:"items"`
		Claimable int           `json:"claimable"`
		Pending   int           `json:"pending"`
		Manual    int           `json:"manual"`
		Ignored   int           `json:"ignored"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Claimable != 1 || got.Manual != 1 || got.Ignored != 1 || got.Pending != 0 {
		t.Errorf("计数 claimable=%d pending=%d manual=%d ignored=%d want 1/0/1/1", got.Claimable, got.Pending, got.Manual, got.Ignored)
	}
	if len(got.Items) != 3 {
		t.Errorf("items=%d want 3（忽略的仍保留在清单）", len(got.Items))
	}
}
