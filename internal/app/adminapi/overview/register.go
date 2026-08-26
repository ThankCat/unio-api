package overview

import "github.com/go-chi/chi/v5"

// Deps 是概览（工作台看板）模块的路由依赖。
type Deps struct {
	Service     DashboardService
	LiveTraffic LiveTrafficService
}

// Register 注册概览模块路由（M9 工作台看板：雷达 / 分组表现 / 性能时序 / 实时流量）。
func Register(r chi.Router, d Deps) {
	if d.LiveTraffic != nil {
		lth := &liveTrafficHandler{service: d.LiveTraffic}
		// 与其余 dashboard 接口的时间尺度不同：这个是 Redis 秒级快照，不是历史聚合。
		r.Get("/dashboard/live-traffic", lth.live)
	}
	if d.Service == nil {
		return
	}
	dh := &dashboardHandler{service: d.Service}
	r.Get("/dashboard/timeseries", dh.timeseries)
	r.Get("/dashboard/radar", dh.radar)
	r.Get("/dashboard/breakdown", dh.breakdown)
	r.Get("/dashboard/errors", dh.topErrors)
	r.Get("/dashboard/timeseries/performance", dh.performanceTimeseries)
}
