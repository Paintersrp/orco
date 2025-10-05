package stack

// CloneServiceMap creates a deep copy of the provided service map.
func CloneServiceMap(services map[string]*Service) map[string]*Service {
	if len(services) == 0 {
		return nil
	}
	out := make(map[string]*Service, len(services))
	for name, svc := range services {
		if svc == nil {
			out[name] = nil
			continue
		}
		out[name] = svc.Clone()
	}
	return out
}
