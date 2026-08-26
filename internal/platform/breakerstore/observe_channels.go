package breakerstore

import (
	"context"
	"errors"
	"fmt"
)

// ObservedChannelRuntime 是管理台观测用的渠道运行态，不参与任何准入判断。
//
// 与 CandidateSnapshot 的差别是刻意的：那个结构服务选路，带 (channel, model) 权限与
// revision 对齐结论；这个只回答「此刻这条渠道熔断没有、并发占了多少、还在不在冷却」，
// 因此不带 model 维度，也不对 revision 下判断。
type ObservedChannelRuntime struct {
	ChannelID int64
	Breaker   ScopeSnapshot
	// Concurrency.Used 为 -1 表示并发计数 key 类型异常，读数不可信。
	Concurrency         CapacityUsage
	CooldownRemainingMs int64
	// CapacityKnown 为 false 表示容量 control 缺失或无法解析，Limit 不可信。
	// 这与 Limit=0 不是一回事：0 的含义是「不限并发」。
	CapacityKnown bool
}

// ConcurrencyKnown 表示并发占用读数是否可信。
func (o ObservedChannelRuntime) ConcurrencyKnown() bool { return o.Concurrency.Used >= 0 }

// ObserveChannels 批量读取若干渠道的运行态，供管理台展示。
//
// 它不校验完整性 epoch，也不校验任何 control revision：观测读到什么算什么，
// 某条渠道的 control 缺失只会把那一行标成 CapacityKnown=false，不影响其余渠道。
// 这与 SnapshotMany 的 fail-closed 语义相反，因为后者的失败是在保护准入决策，
// 而这里失败只会让管理员什么都看不到。
func (s *Store) ObserveChannels(ctx context.Context, channelIDs []int64) (rows []ObservedChannelRuntime, err error) {
	done := s.beginOperation(ctx, operationObserveChannels)
	defer func() { done(operationResultSuccess, err) }()

	if len(channelIDs) == 0 {
		return nil, nil
	}
	if len(channelIDs) > maxSnapshotCandidates {
		return nil, configInvalid("observe channel batch exceeds the maximum")
	}

	keys := make([]string, 0, 1+len(channelIDs)*4)
	keys = append(keys, s.keys.admissionGlobalConcurrency())
	for _, id := range channelIDs {
		if id <= 0 {
			return nil, configInvalid("observe channel id must be positive")
		}
		keys = append(keys,
			s.keys.channel(id),
			s.keys.channelConcurrency(id),
			s.keys.channel429Cooldown(id),
			s.keys.channelCapacity(id),
		)
	}

	res, err := s.observeChannels.Run(ctx, s.client, keys, len(channelIDs)).Result()
	if err != nil {
		return nil, storeUnavailable(err, "breakerstore observe channels")
	}
	parsed, err := parseObserveChannelsReply(channelIDs, res)
	if err != nil {
		return nil, storeUnavailable(err, "breakerstore observe channels")
	}
	return parsed, nil
}

func parseObserveChannelsReply(channelIDs []int64, value interface{}) ([]ObservedChannelRuntime, error) {
	reply, ok := value.([]interface{})
	if !ok || len(reply) != 3 {
		return nil, errors.New("unexpected observe channels reply")
	}
	status, ok := redisString(reply[0])
	if !ok || status != "ok" {
		return nil, fmt.Errorf("unexpected observe channels status %v", reply[0])
	}
	rawRows, ok := reply[2].([]interface{})
	if !ok || len(rawRows) != len(channelIDs) {
		return nil, errors.New("observe channels row count does not match the request")
	}

	out := make([]ObservedChannelRuntime, 0, len(rawRows))
	for index, rawRow := range rawRows {
		row, ok := rawRow.([]interface{})
		if !ok || len(row) != 5 {
			return nil, errors.New("unexpected observe channels row shape")
		}
		used, ok := redisInt64(row[0])
		if !ok {
			return nil, errors.New("observe channels concurrency used is not an integer")
		}
		limit, ok := redisInt64(row[1])
		if !ok || limit < 0 {
			return nil, errors.New("observe channels concurrency limit is invalid")
		}
		capacityKnown, ok := redisInt64(row[2])
		if !ok {
			return nil, errors.New("observe channels capacity flag is not an integer")
		}
		cooldown, ok := redisInt64(row[3])
		if !ok || cooldown < 0 {
			return nil, errors.New("observe channels cooldown is invalid")
		}
		breaker, err := parseSnapshotRow(ScopeChannel, channelIDs[index], row[4])
		if err != nil {
			return nil, err
		}
		out = append(out, ObservedChannelRuntime{
			ChannelID:           channelIDs[index],
			Breaker:             breaker,
			Concurrency:         CapacityUsage{Used: used, Limit: limit},
			CooldownRemainingMs: cooldown,
			CapacityKnown:       capacityKnown == 1,
		})
	}
	return out, nil
}
