package scheduler

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestIsLotteryDisabled 「lottery disabled」是活动状态而非故障：识别后静默收口，
// 避免每次签到为每个账号刷一条错误日志（2026-10 实测 code=400 / msg="lottery disabled"）。
func TestIsLotteryDisabled(t *testing.T) {
	if !isLotteryDisabled(&upstream.Error{Status: 400, Msg: `{"code":400,"msg":"lottery disabled","requestId":"x"}`}) {
		t.Error("应识别 lottery disabled")
	}
	if isLotteryDisabled(&upstream.Error{Status: 500, Msg: "internal error"}) {
		t.Error("普通错误不该被当成抽奖关闭")
	}
	if isLotteryDisabled(nil) {
		t.Error("nil 不该命中")
	}
}
