package storage

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Channels 渠道仓库。
type Channels struct{ db *gorm.DB }

type ChannelInUseError struct {
	MainStationMembers   int64
	UpstreamSyncAccounts int64
}

func (e *ChannelInUseError) Error() string {
	return fmt.Sprintf("渠道仍被账号配置引用：主站账号 %d 条，旧同步账号 %d 条，请先解除关联", e.MainStationMembers, e.UpstreamSyncAccounts)
}

func NewChannels(db *gorm.DB) *Channels { return &Channels{db: db} }

func (r *Channels) Create(c *Channel) error { return r.db.Create(c).Error }
func (r *Channels) Update(c *Channel) error { return r.db.Save(c).Error }
func (r *Channels) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.Select("id", "name").First(&channel, id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var mainStationMembers int64
		if err := tx.Model(&MainAccountPoolMember{}).Where("source_channel_id = ?", id).Count(&mainStationMembers).Error; err != nil {
			return err
		}
		var upstreamSyncAccounts int64
		if err := tx.Model(&UpstreamSyncAccount{}).Where("source_channel_id = ?", id).Count(&upstreamSyncAccounts).Error; err != nil {
			return err
		}
		if mainStationMembers > 0 || upstreamSyncAccounts > 0 {
			return &ChannelInUseError{MainStationMembers: mainStationMembers, UpstreamSyncAccounts: upstreamSyncAccounts}
		}
		if err := tx.Where("channel_id = ?", id).Delete(&AuthSession{}).Error; err != nil {
			return err
		}
		for _, model := range []any{
			&RateSnapshot{},
			&RateChangeLog{},
			&BalanceSnapshot{},
			&CostSnapshot{},
			&MonitorLog{},
			&NotificationCooldown{},
			&UpstreamAnnouncement{},
		} {
			if err := tx.Where("channel_id = ?", id).Delete(model).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("upstream_channel_id = ?", id).Delete(&NotificationLog{}).Error; err != nil {
			return err
		}
		if err := tx.Where("upstream_channel_id = ?", id).Delete(&AlertEvent{}).Error; err != nil {
			return err
		}
		if channel.Name != "" {
			pattern := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(channel.Name) + "%"
			if err := tx.Where("upstream_channel_id = 0 AND (subject LIKE ? ESCAPE '!' OR body LIKE ? ESCAPE '!')", pattern, pattern).
				Delete(&NotificationLog{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&Channel{}, id).Error
	})
}
func (r *Channels) FindByID(id uint) (*Channel, error) {
	var c Channel
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}
func (r *Channels) List() ([]Channel, error) {
	var list []Channel
	if err := orderChannelCards(r.db).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
func (r *Channels) ListPage(page, pageSize int) ([]Channel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 && pageSize != -1 {
		pageSize = 20
	}
	var total int64
	if err := r.db.Model(&Channel{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []Channel
	q := orderChannelCards(r.db)
	if pageSize != -1 {
		q = q.Offset((page - 1) * pageSize).Limit(pageSize)
	}
	if err := q.Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func orderChannelCards(db *gorm.DB) *gorm.DB {
	return db.Order("starred DESC").Order("COALESCE(today_cost, 0) DESC").Order("sort_order DESC").Order("id ASC")
}

func (r *Channels) ListMonitorEnabled() ([]Channel, error) {
	var list []Channel
	if err := r.db.Where("monitor_enabled = ?", true).Order("sort_order DESC").Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
func (r *Channels) UpdateBalance(id uint, balance float64, at any, lastErr string) error {
	return r.db.Model(&Channel{}).Where("id = ?", id).Updates(map[string]any{
		"last_balance":    balance,
		"last_balance_at": at,
		"last_error":      lastErr,
	}).Error
}

func (r *Channels) UpdateCosts(id uint, todayCost float64, totalCost float64) error {
	return r.db.Model(&Channel{}).Where("id = ?", id).Updates(map[string]any{
		"today_cost": todayCost,
		"total_cost": totalCost,
	}).Error
}
func (r *Channels) SetLastError(id uint, msg string) error {
	return r.db.Model(&Channel{}).Where("id = ?", id).Update("last_error", msg).Error
}
