package languages

type Registry struct {
	specs map[string]Spec
}

func NewRegistry(specs ...Spec) *Registry {
	m := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		m[spec.Name()] = spec
	}

	return &Registry{specs: m}
}

func (r *Registry) Get(name string) (Spec, bool) {
	spec, ok := r.specs[name]
	return spec, ok
}
