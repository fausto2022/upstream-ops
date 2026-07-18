package mainstation

import "testing"

func TestRankSchedulingSignalsUsesHealthPriorityCostAndStability(t *testing.T) {
	signals := []schedulingRankSignal{
		{MemberID: 1, HealthBand: 0, Priority: 1, CostKnown: true, CostMicros: 2_000_000, SuccessBucket: 0, LatencyBucket: 0},
		{MemberID: 2, HealthBand: 0, Priority: 1, CostKnown: true, CostMicros: 1_000_000, SuccessBucket: 2, LatencyBucket: 2},
		{MemberID: 3, HealthBand: 0, Priority: 8, CostKnown: true, CostMicros: 500_000, SuccessBucket: 0, LatencyBucket: 0},
		{MemberID: 4, HealthBand: 2, Priority: 1, CostKnown: true, CostMicros: 100_000, SuccessBucket: 0, LatencyBucket: 0},
		{MemberID: 5, HealthBand: 0, Priority: 1, CostKnown: true, CostMicros: 1_000_000, SuccessBucket: 2, LatencyBucket: 2},
	}

	priorities := rankSchedulingSignals(signals)
	if priorities[2] != 1 || priorities[5] != 1 {
		t.Fatalf("same healthy priority signals should share first rank: %#v", priorities)
	}
	if priorities[1] != 2 {
		t.Fatalf("higher-cost account rank = %d, want 2", priorities[1])
	}
	if priorities[3] != 3 {
		t.Fatalf("lower manual priority account rank = %d, want 3", priorities[3])
	}
	if priorities[4] != 4 {
		t.Fatalf("degraded account rank = %d, want 4", priorities[4])
	}
}

func TestRankSchedulingSignalsPrefersTaggedHealthyAccounts(t *testing.T) {
	signals := []schedulingRankSignal{
		{MemberID: 1, HealthBand: 0, Priority: 1, CostKnown: true, CostMicros: 100_000, SuccessBucket: 0, LatencyBucket: 0},
		{MemberID: 2, HealthBand: 0, Preferred: true, Priority: 99, CostKnown: true, CostMicros: 9_000_000, SuccessBucket: 3, LatencyBucket: 3},
		{MemberID: 3, HealthBand: 3, Preferred: true, Priority: 1, CostKnown: true, CostMicros: 10_000, SuccessBucket: 0, LatencyBucket: 0},
	}

	priorities := rankSchedulingSignals(signals)
	if priorities[2] != 1 || priorities[1] != 2 || priorities[3] != 3 {
		t.Fatalf("preferred scheduling order = %#v", priorities)
	}
}

func TestAutomaticSchedulingDefaults(t *testing.T) {
	if normalizeSchedulingPriority(0) != 1 || normalizeSchedulingPriority(-1) != 1 {
		t.Fatal("invalid priority must default to 1")
	}
	if normalizeSchedulingPriority(99) != 99 {
		t.Fatal("positive numeric priority must be preserved")
	}
	if automaticLoadFactor(37) != 37 || automaticLoadFactor(0) != 1 {
		t.Fatal("automatic load factor must follow concurrency")
	}
}
