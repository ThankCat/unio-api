package failure

import "go.uber.org/zap"

// LogFields 将 error 展开成 zap 可直接接收的字段列表。
func LogFields(err error) []zap.Field {
	if err == nil {
		return nil
	}

	fields := []zap.Field{
		zap.Error(err),
	}

	code := CodeOf(err)
	if code == "" {
		return fields
	}

	fields = append(fields,
		zap.String("error_code", string(code)),
		zap.String("error_category", string(code.Category())),
	)

	for _, field := range FieldsOf(err) {
		if field.Key == "" {
			continue
		}

		fields = append(fields, zap.Any(field.Key, field.Value))
	}

	return fields
}
