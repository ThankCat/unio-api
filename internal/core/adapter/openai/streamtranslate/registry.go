package streamtranslate

// Registry 按 provider slug 选择 stream translator。
type Registry struct {
	defaultTranslator StreamTranslator
	bySlug            map[Key]StreamTranslator
}

// NewRegistry 创建 stream translator 注册表；defaultTranslator 不能为空。
func NewRegistry(defaultTranslator StreamTranslator, vendors ...StreamTranslator) *Registry {
	bySlug := make(map[Key]StreamTranslator, len(vendors))
	for _, v := range vendors {
		bySlug[v.Key()] = v
	}

	return &Registry{
		defaultTranslator: defaultTranslator,
		bySlug:            bySlug,
	}
}

// Resolve 按 provider slug 解析 stream translator；未知 slug 回退 default。
func (r *Registry) Resolve(providerSlug string) StreamTranslator {
	if r == nil {
		return nil
	}

	if t, ok := r.bySlug[Key(providerSlug)]; ok {
		return t
	}

	return r.defaultTranslator
}
