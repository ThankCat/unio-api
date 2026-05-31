package normalizer

// Registry 按 provider slug 选择 Normalizer。
type Registry struct {
	defaultNorm Normalizer
	bySlug      map[Key]Normalizer
}

// NewRegistry 创建 normalizer 注册表；defaultNorm 不能为空。
func NewRegistry(defaultNorm Normalizer, vendors ...Normalizer) *Registry {
	bySlug := make(map[Key]Normalizer, len(vendors))
	for _, v := range vendors {
		bySlug[v.Key()] = v
	}

	return &Registry{
		defaultNorm: defaultNorm,
		bySlug:      bySlug,
	}
}

// Resolve 按 provider slug 解析 normalizer；未知 slug 回退 default。
func (r *Registry) Resolve(providerSlug string) Normalizer {
	if r == nil {
		return nil
	}

	if n, ok := r.bySlug[Key(providerSlug)]; ok {
		return n
	}
	
	return r.defaultNorm
}
