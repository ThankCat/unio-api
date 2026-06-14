package chatcompletions

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}

	v := s
	return &v
}
