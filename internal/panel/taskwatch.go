// taskwatch.go 未适配任务检测（「上游上了新任务，网关还不会做」的雷达）。
//
// 背景：上游的成长任务清单是**服务端下发**的，新增任务（例如 2026-10 那批
// expert_use_*）不会通知任何客户端；面板的自动动作是一张白名单（autoActions），
// 白名单外的任务既不会被扫描成待办、也不会被执行——只能靠人盯页面发现。
// 后果是静默的：任务下发、进度永远 0/1，积分停在原地。
//
// 本文件补的是这条观测链路：周期性补领清扫（tasksweep）本来就会拉每个账号的任务
// 列表，顺手比对白名单即可，不额外发请求。
//
// 语义（刻意分三态，避免把噪音当告警）：
//   - 未达标（current < target）：只是"新任务"，进清单但不算紧急；
//   - 达标未领（claimable）：**奖励正在损失**，最高优先级；
//   - locked（上游未开放）：不记录，上游自己还没放出来，报了也没法做。
//
// 已知需要真人操作的任务（manualTaskCodes）单独标注，不进"待适配"计数——
// 例如微信公众号订阅要求真实关注满 24 小时，纯 API 无解。
//
// 持久化到 state.json 同目录的 unknown_tasks.json：重启不丢，去重靠 task_code；
// 面板可以「忽略」某个 code（记 ignored=true），忽略后不再计入提醒但仍在清单里。
package panel

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// manualTaskCodes 上游下发但纯 API 无法完成的任务：报"需人工"，不算待适配。
var manualTaskCodes = map[string]string{
	"wb_wechat_oa_subscribe_task": "需在微信里关注公众号并满 24 小时，面板无法代做",
}

// unknownTask 单个未适配任务的聚合状态（跨账号）。
type unknownTask struct {
	Code       string   `json:"task_code"`
	Title      string   `json:"title,omitempty"`
	Desc       string   `json:"description,omitempty"`
	FirstSeen  int64    `json:"first_seen"`
	LastSeen   int64    `json:"last_seen"`
	UIDs       []string `json:"uids,omitempty"`
	Target     int64    `json:"target"`
	MaxCurrent int64    `json:"max_current"`
	Claimable  bool     `json:"claimable,omitempty"`
	Ignored    bool     `json:"ignored,omitempty"`
	Manual     bool     `json:"manual,omitempty"`
	ManualNote string   `json:"manual_note,omitempty"`
}

// taskWatch 未适配任务的内存状态 + 落盘。
type taskWatch struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	items   map[string]*unknownTask
	lastErr string
}

// watchState 惰性初始化（首次使用时按 StateFile 同目录定位文件）。
func (p *Panel) watchState() *taskWatch {
	p.watchOnce.Do(func() {
		w := &taskWatch{items: map[string]*unknownTask{}}
		if p.cfg.StateFile != "" {
			w.path = filepath.Join(filepath.Dir(p.cfg.StateFile), "unknown_tasks.json")
		}
		p.watch = w
	})
	return p.watch
}

// load 读盘（只做一次）；文件不存在/损坏都按空处理，不影响主流程。
func (w *taskWatch) load() {
	if w.loaded || w.path == "" {
		w.loaded = true
		return
	}
	w.loaded = true
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var items []unknownTask
	if err := json.Unmarshal(raw, &items); err != nil {
		log.Printf("panel: 未适配任务清单解析失败（按空处理）: %v", err)
		return
	}
	for i := range items {
		it := items[i]
		if it.Code == "" {
			continue
		}
		w.items[it.Code] = &it
	}
}

// save 原子落盘（0600）；失败只记一行，不阻塞任务链路。
func (w *taskWatch) save() {
	if w.path == "" {
		return
	}
	items := make([]unknownTask, 0, len(w.items))
	for _, it := range w.items {
		items = append(items, *it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Code < items[j].Code })
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.logSaveErr(err)
		return
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		w.logSaveErr(err)
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		w.logSaveErr(err)
		_ = os.Remove(tmp)
	}
}

// logSaveErr 落盘失败每轮只吵一次（15 分钟一轮，避免刷屏）。
func (w *taskWatch) logSaveErr(err error) {
	msg := err.Error()
	if msg == w.lastErr {
		return
	}
	w.lastErr = msg
	log.Printf("panel: 未适配任务清单落盘失败（不影响任务执行）: %v", err)
}

// note 把一轮任务列表里的"白名单之外"任务并入清单。返回本轮**首次发现**的 code，
// 供调用方打一行日志（只报新出现的，避免每 15 分钟刷一遍）。
func (w *taskWatch) note(uid string, tasks []upstream.Task) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()

	now := time.Now().Unix()
	var fresh []string
	changed := false
	for _, t := range tasks {
		code := t.TaskCode
		if code == "" || t.Claimed || t.Locked {
			continue
		}
		if autoActionFor(code) != nil {
			continue
		}
		it := w.items[code]
		if it == nil {
			it = &unknownTask{Code: code, FirstSeen: now, Target: t.Target}
			w.items[code] = it
			fresh = append(fresh, code)
			changed = true
		}
		if it.Title == "" {
			it.Title = t.Title
		}
		if it.Desc == "" {
			it.Desc = t.Description
		}
		if note, ok := manualTaskCodes[code]; ok {
			it.Manual = true
			it.ManualNote = note
		}
		if t.Target > 0 {
			it.Target = t.Target
		}
		if t.Current > it.MaxCurrent {
			it.MaxCurrent = t.Current
		}
		wasClaimable := it.Claimable
		it.Claimable = it.Target > 0 && it.MaxCurrent >= it.Target
		if it.Claimable && !wasClaimable {
			log.Printf("panel: WARN 未适配任务已达标未领 %s（%q）：面板没有对应自动动作，奖励领不到，需要适配", code, it.Title)
			changed = true
		}
		it.LastSeen = now
		if uid != "" {
			found := false
			for _, u := range it.UIDs {
				if u == uid {
					found = true
					break
				}
			}
			if !found {
				it.UIDs = append(it.UIDs, uid)
			}
		}
	}
	if changed {
		w.save()
	}
	return fresh
}

// snapshot 返回按「紧急度」排序的副本：达标未领 → 待适配 → 需人工 → 已忽略。
func (w *taskWatch) snapshot() []unknownTask {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	out := make([]unknownTask, 0, len(w.items))
	for _, it := range w.items {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		rank := func(t unknownTask) int {
			switch {
			case t.Ignored:
				return 3
			case t.Manual:
				return 2
			case t.Claimable:
				return 0
			default:
				return 1
			}
		}
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// setIgnored 标记/取消忽略某个 task_code（忽略后不计入提醒，但仍保留在清单里）。
func (w *taskWatch) setIgnored(code string, ignored bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	it := w.items[code]
	if it == nil {
		return false
	}
	it.Ignored = ignored
	w.save()
	return true
}

// ---------------------------------------------------------------------------
// HTTP 接口
// ---------------------------------------------------------------------------

// tasksUnknown 未适配任务清单（只读）。claimable = 达标未领（奖励正在损失），
// pending = 只见任务、进度还没到；manual = 需真人操作（如微信订阅）。
func (p *Panel) tasksUnknown(w http.ResponseWriter, r *http.Request) {
	items := p.watchState().snapshot()
	var claimable, pending, manual, ignored int
	for _, it := range items {
		if it.Ignored {
			ignored++
			continue
		}
		switch {
		case it.Manual:
			manual++
		case it.Claimable:
			claimable++
		default:
			pending++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"claimable":  claimable,
		"pending":    pending,
		"manual":     manual,
		"ignored":    ignored,
		"updated_at": time.Now().Unix(),
	})
}

// taskUnknownIgnore 标记/取消忽略某个 task_code（忽略后不再计入提醒，仍留在清单里）。
func (p *Panel) taskUnknownIgnore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskCode string `json:"task_code"`
		Ignored  bool   `json:"ignored"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskCode == "" {
		writeErr(w, http.StatusBadRequest, "task_code required")
		return
	}
	if !p.watchState().setIgnored(body.TaskCode, body.Ignored) {
		writeErr(w, http.StatusNotFound, "unknown task_code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
