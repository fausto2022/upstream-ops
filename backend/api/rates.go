package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/storage"
	"github.com/gin-gonic/gin"
)

type rateChangeOutput struct {
	ID                     uint      `json:"id"`
	ChannelID              uint      `json:"channel_id"`
	ModelName              string    `json:"model_name"`
	OldRatio               *float64  `json:"old_ratio,omitempty"`
	NewRatio               float64   `json:"new_ratio"`
	OldCompletionRatio     *float64  `json:"old_completion_ratio,omitempty"`
	NewCompletionRatio     float64   `json:"new_completion_ratio"`
	RawOldRatio            *float64  `json:"raw_old_ratio,omitempty"`
	RawNewRatio            float64   `json:"raw_new_ratio"`
	RawOldCompletionRatio  *float64  `json:"raw_old_completion_ratio,omitempty"`
	RawNewCompletionRatio  float64   `json:"raw_new_completion_ratio"`
	RechargeAdjusted       bool      `json:"recharge_adjusted"`
	RechargeMultiplier     *float64  `json:"recharge_multiplier,omitempty"`
	RechargeMultiplierMode string    `json:"recharge_multiplier_mode,omitempty"`
	ChangedAt              time.Time `json:"changed_at"`
}

func rateChangeOutputs(list []storage.RateChangeLog, channels []storage.Channel) []rateChangeOutput {
	channelMap := make(map[uint]storage.Channel, len(channels))
	for _, channelItem := range channels {
		channelMap[channelItem.ID] = channelItem
	}

	out := make([]rateChangeOutput, 0, len(list))
	for _, item := range list {
		view := rateChangeOutput{
			ID:                    item.ID,
			ChannelID:             item.ChannelID,
			ModelName:             item.ModelName,
			OldRatio:              copyFloat64(item.OldRatio),
			NewRatio:              item.NewRatio,
			OldCompletionRatio:    copyFloat64(item.OldCompletionRatio),
			NewCompletionRatio:    item.NewCompletionRatio,
			RawOldRatio:           copyFloat64(item.OldRatio),
			RawNewRatio:           item.NewRatio,
			RawOldCompletionRatio: copyFloat64(item.OldCompletionRatio),
			RawNewCompletionRatio: item.NewCompletionRatio,
			ChangedAt:             item.ChangedAt,
		}
		if channelItem, ok := channelMap[item.ChannelID]; ok && channelItem.RechargeMultiplier != nil && *channelItem.RechargeMultiplier > 0 {
			view.RechargeAdjusted = true
			view.RechargeMultiplier = copyFloat64(channelItem.RechargeMultiplier)
			view.RechargeMultiplierMode = connector.NormalizeRechargeMultiplierMode(channelItem.RechargeMultiplierMode)
			view.OldRatio = applyRechargeMultiplierToOptionalRatio(item.OldRatio, &channelItem)
			view.NewRatio = connector.ApplyRechargeMultiplier(item.NewRatio, channelItem.RechargeMultiplier, channelItem.RechargeMultiplierMode)
			view.OldCompletionRatio = applyRechargeMultiplierToOptionalRatio(item.OldCompletionRatio, &channelItem)
			view.NewCompletionRatio = connector.ApplyRechargeMultiplier(item.NewCompletionRatio, channelItem.RechargeMultiplier, channelItem.RechargeMultiplierMode)
		}
		out = append(out, view)
	}
	return out
}

func applyRechargeMultiplierToOptionalRatio(value *float64, channelItem *storage.Channel) *float64 {
	if value == nil {
		return nil
	}
	adjusted := connector.ApplyRechargeMultiplier(*value, channelItem.RechargeMultiplier, channelItem.RechargeMultiplierMode)
	return &adjusted
}

func copyFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func registerRates(g *gin.RouterGroup, d *Deps) {
	g.GET("/rate-changes", func(c *gin.Context) {
		var channelID uint
		if s := c.Query("channel_id"); s != "" {
			id, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, err)
				return
			}
			channelID = uint(id)
		}
		page, pageSize, err := parsePageQuery(c)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		list, total, err := d.Rates.ListChangesPage(channelID, page, pageSize)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		var channels []storage.Channel
		if d.Channels != nil {
			channels, err = d.Channels.List()
			if err != nil {
				fail(c, http.StatusInternalServerError, err)
				return
			}
		}
		pages := 1
		if total > 0 {
			pages = int((total + int64(pageSize) - 1) / int64(pageSize))
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items":     rateChangeOutputs(list, channels),
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"pages":     pages,
		}})
	})
}
