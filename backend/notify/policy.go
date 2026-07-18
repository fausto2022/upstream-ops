package notify

import (
	"fmt"
	"math"
	"time"

	"github.com/fausto2022/relaydeck/backend/storage"
)

// Policy 通知去抖策略。所有字段都是面向"少烦用户"取向：
//   - BatchRateChanges：同次扫描中合并多条倍率相关通知
//   - MinChangePct：涨跌幅小于阈值时跳过推送（仍写入 RateChangeLog 表）
//   - BalanceLowCooldown：同渠道 balance_low 在窗口内不重复发送
//   - SendMaxAttempts：单条消息最多发送尝试次数（含首发），<=1 表示不重试
type Policy struct {
	BatchRateChanges                         bool
	MinChangePct                             float64
	BalanceLowCooldown                       time.Duration
	SubscriptionDailyRemainingThresholdPct   float64
	SubscriptionWeeklyRemainingThresholdPct  float64
	SubscriptionMonthlyRemainingThresholdPct float64
	SubscriptionExpiryThreshold              time.Duration
	SubscriptionAlertCooldown                time.Duration
	SendMaxAttempts                          int
}

// CooldownStore Dispatcher 用来判断某个 (channelID, event) 是否还在冷却窗口。
//
// 抽象成 interface 是为了让 dispatcher 不依赖具体存储；
// 生产实现是 *storage.Notifications.TryClaimCooldown；
// 测试时可以注入一个内存 stub。
type CooldownStore interface {
	TryClaimCooldown(channelID uint, event storage.NotificationEvent, cooldown time.Duration) (bool, error)
}

// RateChange 是一条待发送的倍率相关记录（去抖 / 合并的基本单元）。
type RateChange struct {
	GroupName string
	OldRatio  float64
	NewRatio  float64
	OldComp   float64
	NewComp   float64
	ChangedAt time.Time
}

type RateStructureChange struct {
	Added   []RateChange
	Removed []RateChange
}

// ChangePctAbove 涨跌幅是否达到阈值。
// minPct = 0 表示不过滤。OldRatio = 0 时按"新出现的分组"处理，永远算"达到阈值"。
func (rc RateChange) ChangePctAbove(minPct float64) bool {
	if minPct <= 0 {
		return true
	}
	if rc.OldRatio == 0 {
		return true
	}
	pct := math.Abs(rc.NewRatio-rc.OldRatio) / math.Abs(rc.OldRatio) * 100
	return pct >= minPct
}

// BuildBatchMessage 把多条 RateChange 合并成一条 notify.Message。
// 当只有 1 条时仍走这个路径，但 Subject / Body 自然退化成单条提醒。
func BuildBatchMessage(channel *storage.Channel, changes []RateChange) Message {
	return BuildRateBatchMessage(channel, storage.EventRateChanged, changes)
}

func BuildRateBatchMessage(channel *storage.Channel, event storage.NotificationEvent, changes []RateChange) Message {
	if len(changes) == 0 {
		return Message{}
	}
	now := time.Now()
	if len(changes) == 1 {
		c := changes[0]
		if event == storage.EventRateAdded {
			return Message{
				Event:     storage.EventRateAdded,
				ChannelID: channel.ID,
				ModelName: c.GroupName,
				Subject:   fmt.Sprintf("新增分组 · %s · %s", channel.Name, c.GroupName),
				Body: MarkdownDetails(
					"检测到新的上游分组。",
					Detail("渠道", channel.Name),
					Detail("分组", c.GroupName),
					Detail("倍率", fmt.Sprintf("%g", c.NewRatio)),
					Detail("补全倍率", fmt.Sprintf("%g", c.NewComp)),
					Detail("发现时间", now.Format("2006-01-02 15:04:05")),
				),
			}
		}
		if event == storage.EventRateRemoved {
			return Message{
				Event:     storage.EventRateRemoved,
				ChannelID: channel.ID,
				ModelName: c.GroupName,
				Subject:   fmt.Sprintf("分组移除 · %s · %s", channel.Name, c.GroupName),
				Body: MarkdownDetails(
					"上游返回结果中已找不到该分组。",
					Detail("渠道", channel.Name),
					Detail("分组", c.GroupName),
					Detail("原倍率", fmt.Sprintf("%g", c.OldRatio)),
					Detail("原补全倍率", fmt.Sprintf("%g", c.OldComp)),
					Detail("发现时间", now.Format("2006-01-02 15:04:05")),
				),
			}
		}
		direction := arrowFor(c.OldRatio, c.NewRatio)
		return Message{
			Event:     storage.EventRateChanged,
			ChannelID: channel.ID,
			ModelName: c.GroupName,
			Subject:   fmt.Sprintf("倍率%s · %s · %s", direction, channel.Name, c.GroupName),
			Body: MarkdownDetails(
				fmt.Sprintf("分组倍率发生%s。", direction),
				Detail("渠道", channel.Name),
				Detail("分组", c.GroupName),
				Detail("倍率", fmt.Sprintf("%g -> %g", c.OldRatio, c.NewRatio)),
				Detail("补全倍率", fmt.Sprintf("%g -> %g", c.OldComp, c.NewComp)),
				Detail("变化时间", now.Format("2006-01-02 15:04:05")),
			),
		}
	}

	items := make([]string, 0, len(changes))
	switch event {
	case storage.EventRateAdded:
		for _, c := range changes {
			items = append(items, fmt.Sprintf("%s：倍率 %s，补全倍率 %s",
				MarkdownCode(c.GroupName), MarkdownCode(fmt.Sprintf("%g", c.NewRatio)), MarkdownCode(fmt.Sprintf("%g", c.NewComp))))
		}
		return Message{
			Event:     storage.EventRateAdded,
			ChannelID: channel.ID,
			ModelName: "",
			Subject:   fmt.Sprintf("新增分组 · %s · %d 项", channel.Name, len(changes)),
			Body: MarkdownDetails(
				"检测到多个新的上游分组。",
				Detail("渠道", channel.Name),
				Detail("新增数量", len(changes)),
				Detail("发现时间", now.Format("2006-01-02 15:04:05")),
			) + MarkdownSection("新增明细", items),
		}
	case storage.EventRateRemoved:
		for _, c := range changes {
			items = append(items, fmt.Sprintf("%s：原倍率 %s，原补全倍率 %s",
				MarkdownCode(c.GroupName), MarkdownCode(fmt.Sprintf("%g", c.OldRatio)), MarkdownCode(fmt.Sprintf("%g", c.OldComp))))
		}
		return Message{
			Event:     storage.EventRateRemoved,
			ChannelID: channel.ID,
			ModelName: "",
			Subject:   fmt.Sprintf("分组移除 · %s · %d 项", channel.Name, len(changes)),
			Body: MarkdownDetails(
				"上游返回结果中已找不到这些分组。",
				Detail("渠道", channel.Name),
				Detail("移除数量", len(changes)),
				Detail("发现时间", now.Format("2006-01-02 15:04:05")),
			) + MarkdownSection("移除明细", items),
		}
	default:
		for _, c := range changes {
			items = append(items, fmt.Sprintf("%s：倍率 %s，补全倍率 %s",
				MarkdownCode(c.GroupName),
				MarkdownCode(fmt.Sprintf("%g -> %g", c.OldRatio, c.NewRatio)),
				MarkdownCode(fmt.Sprintf("%g -> %g", c.OldComp, c.NewComp))))
		}
	}

	// ModelName 在合并消息里没有单一值；填空，订阅过滤改在 Dispatcher 里按"先按订阅切片再合并"处理。
	return Message{
		Event:     storage.EventRateChanged,
		ChannelID: channel.ID,
		ModelName: "",
		Subject:   fmt.Sprintf("倍率变化 · %s · %d 项", channel.Name, len(changes)),
		Body: MarkdownDetails(
			"多个分组的倍率发生变化。",
			Detail("渠道", channel.Name),
			Detail("变动数量", len(changes)),
			Detail("变化时间", now.Format("2006-01-02 15:04:05")),
		) + MarkdownSection("变动明细", items),
	}
}

func BuildRateStructureMessage(channel *storage.Channel, change RateStructureChange) Message {
	total := len(change.Added) + len(change.Removed)
	if channel == nil || total == 0 {
		return Message{}
	}
	now := time.Now()
	addedItems := make([]string, 0, len(change.Added))
	if len(change.Added) > 0 {
		for _, c := range change.Added {
			addedItems = append(addedItems, fmt.Sprintf("%s：倍率 %s，补全倍率 %s",
				MarkdownCode(c.GroupName), MarkdownCode(fmt.Sprintf("%g", c.NewRatio)), MarkdownCode(fmt.Sprintf("%g", c.NewComp))))
		}
	}
	removedItems := make([]string, 0, len(change.Removed))
	if len(change.Removed) > 0 {
		for _, c := range change.Removed {
			removedItems = append(removedItems, fmt.Sprintf("%s：原倍率 %s，原补全倍率 %s",
				MarkdownCode(c.GroupName), MarkdownCode(fmt.Sprintf("%g", c.OldRatio)), MarkdownCode(fmt.Sprintf("%g", c.OldComp))))
		}
	}

	return Message{
		Event:     storage.EventRateStructureChanged,
		ChannelID: channel.ID,
		ModelName: "",
		Subject:   fmt.Sprintf("分组结构变化 · %s · 新增 %d / 移除 %d", channel.Name, len(change.Added), len(change.Removed)),
		Body: MarkdownDetails(
			"上游分组结构发生变化。",
			Detail("渠道", channel.Name),
			Detail("新增数量", len(change.Added)),
			Detail("移除数量", len(change.Removed)),
			Detail("发现时间", now.Format("2006-01-02 15:04:05")),
		) + MarkdownSection("新增分组", addedItems) + MarkdownSection("移除分组", removedItems),
	}
}

func arrowFor(oldV, newV float64) string {
	switch {
	case newV > oldV:
		return "上涨"
	case newV < oldV:
		return "下调"
	default:
		return "调整"
	}
}
