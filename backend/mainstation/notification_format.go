package mainstation

func notificationStatusLabel(status string) string {
	switch status {
	case "healthy":
		return "健康"
	case "active":
		return "启用"
	case "degraded":
		return "降级"
	case "critical":
		return "严重"
	case "risk":
		return "风险"
	case "unhealthy":
		return "异常"
	case "quarantined":
		return "已隔离"
	case "rate_limited":
		return "限流"
	case "orphaned":
		return "绑定失效"
	case "invalid":
		return "无效"
	case "unknown":
		return "未知"
	case "unsupported":
		return "不支持"
	case "":
		return "未记录"
	default:
		return status
	}
}
