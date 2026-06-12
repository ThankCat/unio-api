package capability

import (
	"context"
	"sort"
	"strings"

	core "github.com/ThankCat/unio-api/internal/core/capability"
)

// Profile 是 admin 视角的一个 adapter 能力画像（用 provider:protocol 作稳定 key）。
type Profile struct {
	Key          string
	Provider     string
	Protocol     string
	Declarations []core.Declaration
}

// MaterializeResult 是一次 adapter 画像物化的摘要。
type MaterializeResult struct {
	ModelID      int64
	ProfileKey   string
	Provider     string
	Protocol     string
	Materialized int
}

// SeedService 编排 adapter 能力画像的查询与物化（source=adapter_seed）。
//
// 画像注册表在装配期由已注册 adapter（目前仅 DeepSeek 的 openai/anthropic 两协议）组装注入，
// 避免 core 层耦合 adapter。物化是幂等 upsert，会以 adapter_seed 覆盖目标模型同 key 的既有声明。
type SeedService struct {
	store    core.Store
	profiles map[string]core.AdapterProfile
	order    []string
}

// NewSeedService 用给定 adapter 画像注册表创建物化服务。重复 provider:protocol 仅保留首个。
func NewSeedService(store core.Store, profiles []core.AdapterProfile) *SeedService {
	m := make(map[string]core.AdapterProfile, len(profiles))
	order := make([]string, 0, len(profiles))
	for _, p := range profiles {
		key := profileKey(p)
		if _, dup := m[key]; dup {
			continue
		}
		m[key] = p
		order = append(order, key)
	}
	sort.Strings(order)

	return &SeedService{store: store, profiles: m, order: order}
}

// Profiles 返回全部已注册 adapter 画像（按 key 升序）。
func (s *SeedService) Profiles() []Profile {
	out := make([]Profile, 0, len(s.order))
	for _, key := range s.order {
		p := s.profiles[key]
		out = append(out, Profile{
			Key:          key,
			Provider:     p.Provider,
			Protocol:     p.Protocol,
			Declarations: p.Declarations,
		})
	}
	return out
}

// Materialize 把指定 adapter 画像物化进给定模型的 model_capabilities（source=adapter_seed）。
func (s *SeedService) Materialize(ctx context.Context, modelID int64, profileKeyArg, actor string) (MaterializeResult, error) {
	if modelID <= 0 {
		return MaterializeResult{}, invalidArgument("model_id", "model_id must be positive")
	}
	key := strings.TrimSpace(profileKeyArg)
	profile, ok := s.profiles[key]
	if !ok {
		return MaterializeResult{}, invalidArgument("profile_key", "adapter profile not found")
	}
	if err := ensureModelExists(ctx, s.store, modelID); err != nil {
		return MaterializeResult{}, err
	}

	if err := core.MaterializeAdapterSeed(ctx, s.store, modelID, profile, actorPtr(actor)); err != nil {
		return MaterializeResult{}, err
	}

	return MaterializeResult{
		ModelID:      modelID,
		ProfileKey:   key,
		Provider:     profile.Provider,
		Protocol:     profile.Protocol,
		Materialized: len(profile.Declarations),
	}, nil
}

// profileKey 用 provider:protocol 拼出画像稳定 key。
func profileKey(p core.AdapterProfile) string {
	return p.Provider + ":" + p.Protocol
}
