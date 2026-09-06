package responses

import "github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"

func responsesIntPtr(v *dto.ResponsesInt) *int {
	if v == nil {
		return nil
	}
	n := v.Int()
	return &n
}
