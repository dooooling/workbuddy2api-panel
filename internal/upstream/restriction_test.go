package upstream

import "testing"

// TestIsUpstreamRestriction 风控话术 11140（临时限制，软冷却）vs 无话术 11140
// （硬限制，禁用）vs 11-128（通道校验，不罚号）的三态分流。
// 判据来源：ithtelab/workbuddy-manager #362 生产实测——带风控话术的 11140
// 「同一账号数小时后自动恢复 200」，绝不能硬禁用。
func TestIsUpstreamRestriction(t *testing.T) {
	// 命中：带风控话术的 11140（多语言 displayMsg 形态）
	hit := []string{
		`{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been restricted due to unusual activity","zh":"账号存在异常行为，已被限制使用"},"actions":["EDIT_INPUT","SUBMIT_FEEDBACK"]}`,
		`{"code":"11140","msg":"request illegal","displayMsg":{"zh":"系統偵測到該帳號存在異常使用行為"}}`,
		`{"code":11140,"msg":"request illegal","displayMsg":{"en":"account marked abnormal"}}`,
	}
	for _, body := range hit {
		if !IsUpstreamRestriction(body) {
			t.Errorf("应命中风控限制: %s", body)
		}
		if !IsAccountRestricted(body) {
			t.Errorf("风控限制也应是 AccountRestricted: %s", body)
		}
	}

	// 不命中：无话术的 11140 "request illegal"（历史 global auth_forbidden，硬禁用场景）
	if IsUpstreamRestriction(`{"code":11140,"msg":"request illegal","requestId":"x"}`) {
		t.Error("无风控话术的 11140 不该命中（应走 IsAccountRestricted 硬禁用）")
	}
	if !IsAccountRestricted(`{"code":11140,"msg":"request illegal","requestId":"x"}`) {
		t.Error("无话术 11140 仍是 AccountRestricted")
	}

	// 不命中：11-128 通道校验（健康账号，不罚号）
	if IsUpstreamRestriction(`{"code":"11-128","msg":"request illegal","displayMsg":{"zh":"请求被拦截，请重新发送"}}`) {
		t.Error("11-128 不该命中风控限制")
	}
	if IsAccountRestricted(`{"code":"11-128","msg":"request illegal"}`) {
		t.Error("11-128 不该命中 AccountRestricted")
	}

	// 不命中：11140 的模型级限流变体（ErrSoftRate）
	if IsUpstreamRestriction(`{"code":11140,"msg":"The model provider is rate-limiting requests."}`) {
		t.Error("模型级限流变体不该命中风控限制")
	}
}
