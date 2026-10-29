// blindbox.go 成长中心「开盲盒」（伙伴 gacha）。
//
// 机制（2026-10-29 真机实测，账号 135****2124）：
//   - GET  /activity/growth/energy        → {"balance":N,"total_consumed":N,"total_earned":N}
//   - GET  /activity/growth/buddy/quota   → {"affordable":N,"balance":N,"cost_per_open":10,"max_open_count":5}
//   - POST /activity/growth/buddy/open    → {"count":N,"results":[{instance,template:{name,rarity,...}}]}
//
// 每次消耗 cost_per_open（实测 10）能量，单次调用最多 max_open_count（实测 5）个；
// 能量来源是成长任务奖励与连登档位兑换（例如 7d 档 +2e），所以「兑换 → 开盲盒」要
// 按这个顺序跑，否则刚拿到的能量要等下一轮才用得上。
//
// 响应里的 template.name 就是抽到的伙伴名（R/SR/UR 等），日志直接展示。
package upstream

import (
	"encoding/json"
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const (
	energyPath     = "/activity/growth/energy"
	buddyQuotaPath = "/activity/growth/buddy/quota"
	buddyOpenPath  = "/activity/growth/buddy/open"
)

// BlindboxEnergy 盲盒能量账户。
type BlindboxEnergy struct {
	Balance       int `json:"balance"`
	TotalConsumed int `json:"total_consumed"`
	TotalEarned   int `json:"total_earned"`
}

// BlindboxQuota 当前可开次数与单价。affordable 由上游算好（能量 / 单价，并按单次上限截断）。
type BlindboxQuota struct {
	Affordable   int `json:"affordable"`
	Balance      int `json:"balance"`
	CostPerOpen  int `json:"cost_per_open"`
	MaxOpenCount int `json:"max_open_count"`
}

// BlindboxPrize 单个奖品：template 是伙伴模板（name/rarity），instance 是这一只实例。
type BlindboxPrize struct {
	Instance struct {
		ID int64 `json:"id"`
	} `json:"instance"`
	Template struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Rarity string `json:"rarity"`
	} `json:"template"`
	IsFirst bool `json:"is_first"`
}

// BlindboxResult 一次开盒的结果。
type BlindboxResult struct {
	Count   int             `json:"count"`
	Results []BlindboxPrize `json:"results"`
}

// BlindboxEnergyNow 查询盲盒能量余额。
func (c *Client) BlindboxEnergyNow(a *auth.Auth) (BlindboxEnergy, error) {
	var out BlindboxEnergy
	data, err := c.growthJSON(a, http.MethodGet, energyPath, nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

// BlindboxQuotaNow 查询当前可开次数（affordable）与单次上限。
func (c *Client) BlindboxQuotaNow(a *auth.Auth) (BlindboxQuota, error) {
	var out BlindboxQuota
	data, err := c.growthJSON(a, http.MethodGet, buddyQuotaPath, nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

// OpenBlindbox 一次开 count 个盲盒（调用方负责按 max_open_count 截断）。
func (c *Client) OpenBlindbox(a *auth.Auth, count int) (BlindboxResult, error) {
	var out BlindboxResult
	data, err := c.growthJSON(a, http.MethodPost, buddyOpenPath, map[string]any{"count": count})
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
