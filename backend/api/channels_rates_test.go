package api

import (
	"testing"

	"github.com/fausto2022/relaydeck/backend/connector"
	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestApplyRechargeMultiplierToRates(t *testing.T) {
	multiplier := 2.0
	list := []storage.RateSnapshot{{Ratio: 0.7, CompletionRatio: 1.4}}
	applyRechargeMultiplierToRates(list, &storage.Channel{
		RechargeMultiplier: &multiplier, RechargeMultiplierMode: connector.RechargeMultiplierModeDivide,
	})
	if list[0].Ratio != 0.35 || list[0].CompletionRatio != 0.7 {
		t.Fatalf("divide rates = %#v", list[0])
	}
	applyRechargeMultiplierToRates(list, &storage.Channel{
		RechargeMultiplier: &multiplier, RechargeMultiplierMode: connector.RechargeMultiplierModeMultiply,
	})
	if list[0].Ratio != 0.7 || list[0].CompletionRatio != 1.4 {
		t.Fatalf("multiply rates = %#v", list[0])
	}
}
